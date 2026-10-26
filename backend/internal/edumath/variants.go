package edumath

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// Variant sets (FR-M07, FR-T11, AC-22) and per student extensions (AC-21).
//
// A variant set is produced by the math service job kind "variant_set": numbers only variants of the
// lesson's problem, each re-normalized, re-solved and checked by the family's independent verifier;
// items whose answer repeats the parent's (or another item's) are dropped with the reason recorded.
// The platform stores the items and the answer keys separately, requires a review bound to the set hash
// under the institution review policy, and only lets an approved set be attached to a new assignment.

const maxVariantItems = 5

type variantItem struct {
	ID             string            `json:"id"`
	FamilyID       string            `json:"family_id"`
	Params         map[string]string `json:"params"`
	Unit           string            `json:"unit"`
	Prompt         string            `json:"prompt"`
	AnswerFormat   string            `json:"answer_format"`
	ChangeType     string            `json:"change_type"`
	Transfer       string            `json:"transfer"`
	Method         string            `json:"method"`
	MathState      string            `json:"math_state"`
	EvidenceLevels []string          `json:"evidence_levels"`
	AnswerKey      *answerKey        `json:"answer_key,omitempty"`
}

type answerKey struct {
	ID    string `json:"id"`
	Exact string `json:"exact"`
	Plain string `json:"plain"`
	Latex string `json:"latex"`
}

type variantSetDoc struct {
	FamilyID       string          `json:"family_id"`
	CatalogVersion string          `json:"catalog_version"`
	Requested      int             `json:"requested"`
	Items          []variantItem   `json:"items"`
	Dropped        json.RawMessage `json:"dropped"`
}

func (h *API) registerVariants(g *gin.RouterGroup) {
	g.POST("/lesson-revisions/:id/variant-sets", h.startVariantSet)
	g.GET("/lesson-revisions/:id/variant-sets", h.listVariantSets)
	g.GET("/variant-sets/:id", h.getVariantSet)
	g.POST("/variant-sets/:id/review", h.reviewVariantSet)
	g.GET("/assignments/:id/extensions", h.listExtensions)
	g.POST("/assignments/:id/extensions", h.putExtension)
	g.POST("/assignments/:id/extensions/:studentId/remove", h.removeExtension)
}

// StartVariantSet dispatches a variant_set job for a lesson whose solution is verified.
func (s *Service) StartVariantSet(ctx context.Context, tx *gorm.DB, a Actor, lessonID string, count int, idemKey string) (model.EduMathJob, error) {
	var l model.EduLessonRevision
	if err := tx.First(&l, "id = ? AND tenant_id = ?", lessonID, a.TenantID).Error; err != nil {
		return model.EduMathJob{}, ErrNotFound
	}
	r, _, err := s.revision(tx, a, l.ProblemRevisionID)
	if err != nil {
		return model.EduMathJob{}, err
	}
	if l.BlockingErrors != "" || l.State == "rejected" {
		return model.EduMathJob{}, appErr(409, "VALIDATION_FAILED", "lesson revision has blocking errors; variants are not generated from it", "teacher_fix")
	}
	if count < 1 || count > maxVariantItems {
		return model.EduMathJob{}, appErr(400, "PARAMETER_LIMIT", fmt.Sprintf("count must be 1..%d", maxVariantItems), "")
	}
	var problem map[string]any
	_ = json.Unmarshal([]byte(r.Structured), &problem)
	payload := map[string]any{"problem": problem, "problem_hash": r.InputHash, "count": count}
	job, err := s.startJobWith(ctx, tx, a, "variant_set", idemKey, hashOf("variant_set", l.ID, l.LessonHash, strconv.Itoa(count)), r.ID, l.JobID,
		fmt.Sprintf(`{"count":%d}`, count), payload)
	return job, err
}

func (s *Service) materializeVariantSet(ctx context.Context, tx *gorm.DB, job model.EduMathJob, arts map[string]model.EduArtifact) error {
	var n int64
	tx.Model(&model.EduVariantSet{}).Where("job_id = ?", job.ID).Count(&n)
	if n > 0 {
		return nil
	}
	ta, sa := arts["variant_set"], arts["variant_set_student"]
	if ta.ID == "" || sa.ID == "" {
		return fmt.Errorf("%w: missing variant set artifacts", ErrContract)
	}
	var l model.EduLessonRevision
	if err := tx.First(&l, "job_id = ? AND tenant_id = ?", job.SourceJobID, job.TenantID).Error; err != nil {
		return err
	}
	raw, _, err := s.Client.Artifact(ctx, job.TenantID, ta.ServiceArtifactID, ta.SHA256)
	if err != nil {
		return err
	}
	var doc variantSetDoc
	if json.Unmarshal(raw, &doc) != nil || len(doc.Items) == 0 || len(doc.Items) > maxVariantItems {
		return fmt.Errorf("%w: variant set document", ErrContract)
	}
	keys := map[string]answerKey{}
	var blocking []string
	items := make([]variantItem, 0, len(doc.Items))
	for _, it := range doc.Items {
		if it.AnswerKey == nil || it.AnswerKey.Exact == "" {
			return fmt.Errorf("%w: variant without answer key", ErrContract)
		}
		if it.MathState != "verified" {
			blocking = append(blocking, it.ID+":math_not_verified")
		}
		keys[it.ID] = *it.AnswerKey
		it.AnswerKey = nil
		items = append(items, it)
	}
	ij, _ := json.Marshal(items)
	kj, _ := json.Marshal(keys)
	state, be := "ready_for_review", ""
	if len(blocking) > 0 {
		state = "rejected"
		bj, _ := json.Marshal(blocking)
		be = string(bj)
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.EduVariantSet{ID: flow.NewID("evs_"), TenantID: job.TenantID, WorkspaceID: job.WorkspaceID,
		LessonRevisionID: l.ID, ProblemRevisionID: job.ProblemRevisionID, JobID: job.ID, ArtifactID: ta.ID, StudentArtifactID: sa.ID, SetHash: ta.SHA256,
		FamilyID: doc.FamilyID, Requested: doc.Requested, ItemCount: len(items), ItemsJSON: string(ij), AnswerKeysJSON: string(kj), DroppedJSON: string(doc.Dropped),
		State: state, BlockingErrors: be, CreatedBy: job.CreatedBy, CreatedAt: s.now()}).Error
}

// reviewAllowed applies the institution review policy (FR-I04, AC-03) to any reviewable object.
func (s *Service) reviewAllowed(tx *gorm.DB, a Actor, createdBy string) (self bool, mode string, ok bool) {
	self = createdBy == a.UserID
	mode = s.policy(tx, a.TenantID)
	ok = a.Has("reviewer") && !(self && mode == "second_reviewer")
	if !ok && self && a.Has("teacher") && mode == "self_allowed" {
		ok = true
	}
	return self, mode, ok
}

func (s *Service) ReviewVariantSet(tx *gorm.DB, a Actor, setID, setHash, decision, comment string) (model.EduVariantReview, error) {
	var vs model.EduVariantSet
	if err := tx.First(&vs, "id = ? AND tenant_id = ? AND workspace_id = ?", setID, a.TenantID, a.WorkspaceID).Error; err != nil {
		return model.EduVariantReview{}, ErrNotFound
	}
	if decision != "approved" && decision != "rejected" {
		return model.EduVariantReview{}, appErr(400, "INPUT_AMBIGUOUS", "decision must be approved or rejected", "")
	}
	if vs.SetHash != setHash {
		return model.EduVariantReview{}, appErr(409, "REVISION_CONFLICT", "review must bind the current variant set hash", "reload_revision")
	}
	if vs.State != "ready_for_review" {
		return model.EduVariantReview{}, appErr(409, "REVISION_CONFLICT", "variant set is "+vs.State, "reload_revision")
	}
	self, mode, ok := s.reviewAllowed(tx, a, vs.CreatedBy)
	if !ok {
		return model.EduVariantReview{}, appErr(403, "POLICY_REVOKED", "review not permitted by the institution review policy ("+mode+")", "request_reviewer")
	}
	if decision == "approved" && vs.BlockingErrors != "" {
		return model.EduVariantReview{}, appErr(409, "VALIDATION_FAILED", "variant set has blocking errors", "teacher_fix")
	}
	now := s.now()
	res := tx.Model(&model.EduVariantSet{}).Where("id = ? AND state = ? AND set_hash = ?", vs.ID, "ready_for_review", setHash).
		Updates(map[string]any{"state": decision, "reviewed_at": now})
	if res.Error != nil {
		return model.EduVariantReview{}, res.Error
	}
	if res.RowsAffected == 0 {
		return model.EduVariantReview{}, appErr(409, "REVISION_CONFLICT", "variant set changed during review", "reload_revision")
	}
	rec := model.EduVariantReview{ID: flow.NewID("evr_"), TenantID: a.TenantID, VariantSetID: vs.ID, SetHash: vs.SetHash, ReviewerID: a.UserID,
		SelfReview: self, Decision: decision, Comment: comment, CreatedAt: now}
	if err := tx.Create(&rec).Error; err != nil {
		return rec, err
	}
	s.audit(tx, a, "variants.review."+decision, vs.ID, vs.SetHash)
	return rec, nil
}

func variantSetOut(vs model.EduVariantSet, withKeys bool) gin.H {
	var items []variantItem
	_ = json.Unmarshal([]byte(vs.ItemsJSON), &items)
	out := gin.H{"id": vs.ID, "lessonRevisionId": vs.LessonRevisionID, "jobId": vs.JobID, "setHash": vs.SetHash, "familyId": vs.FamilyID,
		"requested": vs.Requested, "itemCount": vs.ItemCount, "state": vs.State, "blockingErrors": vs.BlockingErrors, "createdBy": vs.CreatedBy,
		"createdAt": vs.CreatedAt, "reviewedAt": vs.ReviewedAt, "items": items}
	if withKeys {
		var keys map[string]answerKey
		_ = json.Unmarshal([]byte(vs.AnswerKeysJSON), &keys)
		out["answerKeys"] = keys
		out["dropped"] = json.RawMessage(nonEmptyJSON(vs.DroppedJSON))
	}
	return out
}

func nonEmptyJSON(s string) string {
	if strings.TrimSpace(s) == "" || s == "null" {
		return "[]"
	}
	return s
}

func (h *API) staffLesson(c *gin.Context, tx *gorm.DB, a Actor, id string) (model.EduLessonRevision, bool) {
	var l model.EduLessonRevision
	if err := tx.First(&l, "id = ? AND tenant_id = ?", id, a.TenantID).Error; err != nil {
		h.fail(c, ErrNotFound)
		return l, false
	}
	if _, _, err := h.Svc.revision(tx, a, l.ProblemRevisionID); err != nil {
		h.fail(c, err)
		return l, false
	}
	return l, true
}

func (h *API) startVariantSet(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var in struct {
		Count          int    `json:"count"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	_ = c.ShouldBindJSON(&in)
	if in.Count == 0 {
		in.Count = 3
	}
	key := c.GetHeader("Idempotency-Key")
	if key == "" {
		key = in.IdempotencyKey
	}
	ctx, cancel := ctx15(c)
	defer cancel()
	j, err := h.Svc.StartVariantSet(ctx, tx, a, c.Param("id"), in.Count, key)
	if err != nil {
		h.fail(c, err)
		return
	}
	reply(c, 202, jobOut(j), "accepted")
}

func (h *API) listVariantSets(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	l, ok := h.staffLesson(c, tx, a, c.Param("id"))
	if !ok {
		return
	}
	var sets []model.EduVariantSet
	tx.Where("tenant_id = ? AND lesson_revision_id = ?", a.TenantID, l.ID).Order("created_at desc").Find(&sets)
	out := make([]gin.H, 0, len(sets))
	for _, vs := range sets {
		out = append(out, variantSetOut(vs, false))
	}
	reply(c, 200, out, "ok")
}

func (h *API) getVariantSet(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var vs model.EduVariantSet
	if tx.First(&vs, "id = ? AND tenant_id = ? AND workspace_id = ?", c.Param("id"), a.TenantID, a.WorkspaceID).Error != nil {
		h.fail(c, ErrNotFound)
		return
	}
	if _, ok := h.staffLesson(c, tx, a, vs.LessonRevisionID); !ok {
		return
	}
	var reviews []model.EduVariantReview
	tx.Where("variant_set_id = ?", vs.ID).Order("created_at").Find(&reviews)
	out := variantSetOut(vs, true)
	out["reviews"] = reviews
	out["reviewMode"] = h.Svc.policy(tx, a.TenantID)
	reply(c, 200, out, "ok")
}

func (h *API) reviewVariantSet(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var in struct {
		SetHash  string `json:"setHash"`
		Decision string `json:"decision"`
		Comment  string `json:"comment"`
	}
	_ = c.ShouldBindJSON(&in)
	if len(in.Comment) > 1000 {
		reply(c, 400, nil, "comment <= 1000 bytes")
		return
	}
	r, err := h.Svc.ReviewVariantSet(tx, a, c.Param("id"), in.SetHash, in.Decision, in.Comment)
	if err != nil {
		h.fail(c, err)
		return
	}
	reply(c, 200, r, "ok")
}

// approvedVariantSetFor validates a set for a new assignment: approved, same tenant/workspace and built
// from exactly the lesson revision the publication is bound to (AC-22: never assigned unless approved).
func approvedVariantSetFor(tx *gorm.DB, a Actor, setID string, pub model.EduPublication) (model.EduVariantSet, error) {
	var vs model.EduVariantSet
	if tx.First(&vs, "id = ? AND tenant_id = ? AND workspace_id = ?", setID, a.TenantID, a.WorkspaceID).Error != nil {
		return vs, ErrNotFound
	}
	if vs.State != "approved" {
		return vs, appErr(409, "VALIDATION_FAILED", "variant set is "+vs.State+"; only an approved set can be assigned", "review_variants")
	}
	if vs.LessonRevisionID != pub.LessonRevisionID {
		return vs, appErr(409, "REVISION_CONFLICT", "variant set belongs to another lesson revision", "reload_revision")
	}
	return vs, nil
}

// studentVariantItems returns the public item fields only; answer keys stay on the server.
func studentVariantItems(tx *gorm.DB, tenantID, setID string) ([]gin.H, map[string]answerKey, error) {
	var vs model.EduVariantSet
	if tx.First(&vs, "id = ? AND tenant_id = ? AND state = ?", setID, tenantID, "approved").Error != nil {
		return nil, nil, appErr(409, "REVISION_CONFLICT", "the self-test set is not available", "contact_teacher")
	}
	var items []variantItem
	var keys map[string]answerKey
	if json.Unmarshal([]byte(vs.ItemsJSON), &items) != nil || json.Unmarshal([]byte(vs.AnswerKeysJSON), &keys) != nil {
		return nil, nil, ErrContract
	}
	out := make([]gin.H, 0, len(items))
	for _, it := range items {
		out = append(out, gin.H{"id": it.ID, "prompt": it.Prompt, "params": it.Params, "unit": it.Unit, "answer_format": it.AnswerFormat})
	}
	return out, keys, nil
}

// ---------------------------------------------------------------------------
// Per student extensions (AC-21)

func (h *API) extensionFor(tx *gorm.DB, as model.EduAssignment, userID string) *time.Time {
	var e model.EduAssignmentExtension
	if userID == "" || tx.First(&e, "tenant_id = ? AND assignment_id = ? AND student_user_id = ?", as.TenantID, as.ID, userID).Error != nil {
		return nil
	}
	d := e.DueAt.UTC()
	return &d
}

// effectiveDue is the later of the assignment due time and the student's extension.
func (h *API) effectiveDue(tx *gorm.DB, as model.EduAssignment, userID string) *time.Time {
	ext := h.extensionFor(tx, as, userID)
	if ext == nil {
		return as.DueAt
	}
	if as.DueAt != nil && as.DueAt.After(*ext) {
		return as.DueAt
	}
	return ext
}

func (h *API) staffAssignmentByID(c *gin.Context, tx *gorm.DB, a Actor) (model.EduAssignment, bool) {
	var as model.EduAssignment
	if tx.First(&as, "id = ? AND tenant_id = ?", c.Param("id"), a.TenantID).Error != nil {
		h.fail(c, ErrNotFound)
		return as, false
	}
	if _, err := classForStaff(tx, a, as.ClassID); err != nil {
		h.fail(c, err)
		return as, false
	}
	return as, true
}

func (h *API) listExtensions(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	as, ok := h.staffAssignmentByID(c, tx, a)
	if !ok {
		return
	}
	var rows []model.EduAssignmentExtension
	tx.Where("tenant_id = ? AND assignment_id = ?", a.TenantID, as.ID).Order("created_at").Find(&rows)
	reply(c, 200, rows, "ok")
}

func (h *API) putExtension(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	as, ok := h.staffAssignmentByID(c, tx, a)
	if !ok {
		return
	}
	var in struct {
		StudentUserID string     `json:"studentUserId"`
		DueAt         *time.Time `json:"dueAt"` // RFC 3339 with an explicit offset; stored in UTC
		Reason        string     `json:"reason"`
	}
	if c.ShouldBindJSON(&in) != nil || in.StudentUserID == "" || in.DueAt == nil || len(in.Reason) > 200 {
		reply(c, 400, nil, "studentUserId and dueAt (RFC 3339 with offset) required; reason <= 200 bytes")
		return
	}
	if as.State != "active" {
		h.fail(c, appErr(409, "ASSIGNMENT_CLOSED", "assignment is "+as.State, ""))
		return
	}
	due := in.DueAt.UTC()
	if as.DueAt == nil || !due.After(*as.DueAt) {
		h.fail(c, appErr(400, "INPUT_AMBIGUOUS", "an extension must be later than the assignment due time", ""))
		return
	}
	// Only students actively enrolled in the assignment's class can get an extension.
	var en model.EduEnrollment
	if tx.First(&en, "tenant_id = ? AND class_id = ? AND student_user_id = ? AND status = ?", a.TenantID, as.ClassID, in.StudentUserID, "active").Error != nil {
		h.fail(c, ErrNotFound)
		return
	}
	now := h.Svc.now()
	row := model.EduAssignmentExtension{ID: flow.NewID("eax_"), TenantID: a.TenantID, AssignmentID: as.ID, StudentUserID: in.StudentUserID, DueAt: due,
		Reason: strings.TrimSpace(in.Reason), CreatedBy: a.UserID, CreatedAt: now, UpdatedAt: now}
	err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "assignment_id"}, {Name: "student_user_id"}},
		DoUpdates: clause.Assignments(map[string]any{"due_at": due, "reason": row.Reason, "created_by": a.UserID, "updated_at": now})}).Create(&row).Error
	if err != nil {
		h.fail(c, err)
		return
	}
	tx.First(&row, "tenant_id = ? AND assignment_id = ? AND student_user_id = ?", a.TenantID, as.ID, in.StudentUserID)
	h.Svc.audit(tx, a, "assignment.extension", as.ID, in.StudentUserID+" due="+due.Format(time.RFC3339))
	reply(c, 200, row, "ok")
}

func (h *API) removeExtension(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	as, ok := h.staffAssignmentByID(c, tx, a)
	if !ok {
		return
	}
	res := tx.Where("tenant_id = ? AND assignment_id = ? AND student_user_id = ?", a.TenantID, as.ID, c.Param("studentId")).Delete(&model.EduAssignmentExtension{})
	if res.Error != nil {
		h.fail(c, res.Error)
		return
	}
	h.Svc.audit(tx, a, "assignment.extension.remove", as.ID, c.Param("studentId"))
	reply(c, 200, gin.H{"removed": res.RowsAffected}, "ok")
}

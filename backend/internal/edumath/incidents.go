package edumath

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// Content incidents and the withdrawal chain (DEV-027, AC-34) plus assignment
// migration (DEV-025). A blocking incident freezes new access in one transaction:
// the publication is withdrawn, the lesson revision can never be published or
// rolled back to again, its assignments are suspended (attempts kept), derived
// variant sets are withdrawn and every student facing artifact is revoked, so
// signed links and caches stop serving immediately (canFetch re-checks
// RevokedAt). A fix must descend from the incident revision and carry a fresh
// human review; an inherited approval never closes an incident.

var incidentSeverities = map[string]bool{"blocking": true, "minor": true}

const maxLineage = 64

type incidentImpact struct {
	Assignments      []gin.H  `json:"assignments"`
	Classes          []string `json:"classes"`
	Students         int64    `json:"students"`
	Attempts         int64    `json:"attempts"`
	VariantSets      []string `json:"variantSets"`
	RevokedArtifacts []string `json:"revokedArtifacts"`
	ChildRevisions   []string `json:"childRevisions"`
}

// lessonForStaff loads a lesson revision of the actor's workspace that the actor may act on as teacher or org admin.
func lessonForStaff(tx *gorm.DB, a Actor, lessonID string) (model.EduLessonRevision, model.EduMathJob, error) {
	var l model.EduLessonRevision
	var job model.EduMathJob
	if tx.First(&l, "id = ? AND tenant_id = ?", lessonID, a.TenantID).Error != nil ||
		tx.First(&job, "id = ? AND tenant_id = ?", l.JobID, a.TenantID).Error != nil || job.WorkspaceID != a.WorkspaceID {
		return l, job, ErrNotFound
	}
	if !a.Has("teacher") && !canManageRoles(tx, a.TenantID, a.UserID) {
		return l, job, ErrNoRole
	}
	return l, job, nil
}

func studentArtifactsOf(l model.EduLessonRevision, sets []model.EduVariantSet) []string {
	var ids []string
	for _, id := range []string{l.StudentArtifact, l.StudentPreview} {
		if id != "" {
			ids = append(ids, id)
		}
	}
	for _, v := range sets {
		if v.StudentArtifactID != "" {
			ids = append(ids, v.StudentArtifactID)
		}
	}
	return ids
}

func computeImpact(tx *gorm.DB, tenant string, l model.EduLessonRevision, pub model.EduPublication) incidentImpact {
	imp := incidentImpact{Assignments: []gin.H{}, Classes: []string{}, VariantSets: []string{}, RevokedArtifacts: []string{}, ChildRevisions: []string{}}
	var as []model.EduAssignment
	tx.Where("tenant_id = ? AND publication_id = ?", tenant, pub.ID).Order("created_at").Find(&as)
	seen := map[string]bool{}
	var ids []string
	for _, x := range as {
		imp.Assignments = append(imp.Assignments, gin.H{"id": x.ID, "classId": x.ClassID, "title": x.Title, "state": x.State})
		ids = append(ids, x.ID)
		if !seen[x.ClassID] {
			seen[x.ClassID] = true
			imp.Classes = append(imp.Classes, x.ClassID)
		}
	}
	if len(imp.Classes) > 0 {
		tx.Model(&model.EduEnrollment{}).Where("tenant_id = ? AND class_id IN ? AND status = 'active'", tenant, imp.Classes).
			Distinct("student_user_id").Count(&imp.Students)
	}
	if len(ids) > 0 {
		tx.Model(&model.EduAttempt{}).Where("tenant_id = ? AND assignment_id IN ?", tenant, ids).Count(&imp.Attempts)
	}
	var sets []model.EduVariantSet
	tx.Where("tenant_id = ? AND lesson_revision_id = ?", tenant, l.ID).Order("created_at").Find(&sets)
	for _, v := range sets {
		imp.VariantSets = append(imp.VariantSets, v.ID)
	}
	imp.RevokedArtifacts = append(imp.RevokedArtifacts, studentArtifactsOf(l, sets)...)
	var kids []model.EduLessonRevision
	tx.Where("tenant_id = ? AND parent_lesson_revision_id = ?", tenant, l.ID).Order("created_at").Find(&kids)
	for _, k := range kids {
		imp.ChildRevisions = append(imp.ChildRevisions, k.ID)
	}
	return imp
}

func incidentOut(in model.EduContentIncident) gin.H {
	var imp any
	_ = json.Unmarshal([]byte(in.ImpactJSON), &imp)
	return gin.H{"incident": in, "impact": imp}
}

// OpenIncident records a content error on a publication. Blocking incidents run
// the whole withdrawal chain in the caller's transaction.
func (s *Service) OpenIncident(tx *gorm.DB, a Actor, pubID, severity, reason string) (model.EduContentIncident, bool, error) {
	var inc model.EduContentIncident
	reason = strings.TrimSpace(reason)
	if !incidentSeverities[severity] || reason == "" || len([]rune(reason)) > 400 {
		return inc, false, appErr(400, "VALIDATION_FAILED", "severity must be blocking or minor and reason 1..400 chars", "fix_input")
	}
	var pub model.EduPublication
	if tx.First(&pub, "id = ? AND tenant_id = ?", pubID, a.TenantID).Error != nil {
		return inc, false, ErrNotFound
	}
	l, job, err := lessonForStaff(tx, a, pub.LessonRevisionID)
	if err != nil {
		return inc, false, err
	}
	if tx.Dialector.Name() == "postgres" {
		tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "edu-incident:"+pub.ID)
	}
	if tx.Where("tenant_id = ? AND publication_id = ? AND state = 'open'", a.TenantID, pub.ID).First(&inc).Error == nil {
		if inc.Severity == severity || inc.Severity == "blocking" {
			return inc, false, nil // idempotent: an open incident already covers this
		}
		// Escalation from minor to blocking runs the chain on the existing incident.
	}
	imp := computeImpact(tx, a.TenantID, l, pub)
	now := s.now()
	if severity == "blocking" {
		if err := tx.Model(&model.EduPublication{}).Where("id = ?", pub.ID).Update("state", "withdrawn").Error; err != nil {
			return inc, false, err
		}
		if err := tx.Model(&model.EduLessonRevision{}).Where("id = ?", l.ID).Update("state", "withdrawn").Error; err != nil {
			return inc, false, err
		}
		if err := tx.Model(&model.EduAssignment{}).Where("tenant_id = ? AND publication_id = ? AND state = 'active'", a.TenantID, pub.ID).
			Update("state", "suspended").Error; err != nil {
			return inc, false, err
		}
		if len(imp.VariantSets) > 0 {
			if err := tx.Model(&model.EduVariantSet{}).Where("tenant_id = ? AND id IN ?", a.TenantID, imp.VariantSets).Update("state", "withdrawn").Error; err != nil {
				return inc, false, err
			}
		}
		if len(imp.RevokedArtifacts) > 0 {
			if err := tx.Model(&model.EduArtifact{}).Where("tenant_id = ? AND id IN ? AND audience = 'student' AND revoked_at IS NULL", a.TenantID, imp.RevokedArtifacts).
				Update("revoked_at", now).Error; err != nil {
				return inc, false, err
			}
		}
	} else {
		imp.RevokedArtifacts = []string{}
	}
	raw, _ := json.Marshal(imp)
	if inc.ID != "" {
		if err := tx.Model(&inc).Updates(map[string]any{"severity": severity, "reason": reason, "impact_json": string(raw)}).Error; err != nil {
			return inc, false, err
		}
		inc.Severity, inc.Reason, inc.ImpactJSON = severity, reason, string(raw)
		s.audit(tx, a, "incident.escalate", inc.ID, pub.ID)
		return inc, true, nil
	}
	inc = model.EduContentIncident{ID: flow.NewID("eci_"), TenantID: a.TenantID, WorkspaceID: job.WorkspaceID, PublicationID: pub.ID, LessonRevisionID: l.ID,
		Severity: severity, Reason: reason, State: "open", ImpactJSON: string(raw), CreatedBy: a.UserID, CreatedAt: now}
	if err := tx.Create(&inc).Error; err != nil {
		return inc, false, err
	}
	s.audit(tx, a, "incident.open."+severity, inc.ID, pub.ID)
	return inc, true, nil
}

// descendsFrom walks the edit lineage of child back to ancestor.
func descendsFrom(tx *gorm.DB, tenant, childID, ancestorID string) bool {
	cur := childID
	for i := 0; i < maxLineage && cur != ""; i++ {
		var l model.EduLessonRevision
		if tx.Select("id", "parent_lesson_revision_id").First(&l, "id = ? AND tenant_id = ?", cur, tenant).Error != nil {
			return false
		}
		if l.ParentLessonRevisionID == ancestorID {
			return true
		}
		cur = l.ParentLessonRevisionID
	}
	return false
}

// ResolveIncident closes an incident. A blocking incident needs a fix
// publication that descends from the incident revision and was freshly reviewed.
func (s *Service) ResolveIncident(tx *gorm.DB, a Actor, id, fixPubID, note string) (model.EduContentIncident, error) {
	var inc model.EduContentIncident
	note = strings.TrimSpace(note)
	if note == "" || len([]rune(note)) > 400 {
		return inc, appErr(400, "VALIDATION_FAILED", "note 1..400 chars is required", "fix_input")
	}
	if tx.First(&inc, "id = ? AND tenant_id = ? AND workspace_id = ?", id, a.TenantID, a.WorkspaceID).Error != nil {
		return inc, ErrNotFound
	}
	if _, _, err := lessonForStaff(tx, a, inc.LessonRevisionID); err != nil {
		return inc, err
	}
	if inc.State != "open" {
		return inc, appErr(409, "REVISION_CONFLICT", "incident already resolved", "reload")
	}
	if inc.Severity == "blocking" || fixPubID != "" {
		var fix model.EduPublication
		if fixPubID == "" || tx.First(&fix, "id = ? AND tenant_id = ? AND state = 'published'", fixPubID, a.TenantID).Error != nil {
			return inc, appErr(409, "VALIDATION_FAILED", "a blocking incident needs a published fix", "publish_fix")
		}
		if !descendsFrom(tx, a.TenantID, fix.LessonRevisionID, inc.LessonRevisionID) {
			return inc, appErr(409, "VALIDATION_FAILED", "the fix must be an edit of the incident revision", "edit_revision")
		}
		var rec model.EduReviewRecord
		if tx.First(&rec, "id = ? AND tenant_id = ?", fix.ReviewRecordID, a.TenantID).Error != nil || rec.InheritedFrom != "" || rec.Decision != "approved" {
			return inc, appErr(409, "VALIDATION_FAILED", "the fix needs a fresh human review; inherited approval does not close an incident", "teacher_review")
		}
		inc.FixPublicationID = fix.ID
	}
	now := s.now()
	inc.State, inc.ResolutionNote, inc.ResolvedBy, inc.ResolvedAt = "resolved", note, a.UserID, &now
	res := tx.Model(&model.EduContentIncident{}).Where("id = ? AND state = 'open'", inc.ID).
		Updates(map[string]any{"state": "resolved", "resolution_note": note, "resolved_by": a.UserID, "resolved_at": now, "fix_publication_id": inc.FixPublicationID})
	if res.Error != nil {
		return inc, res.Error
	}
	if res.RowsAffected == 0 {
		return inc, appErr(409, "REVISION_CONFLICT", "incident already resolved", "reload")
	}
	s.audit(tx, a, "incident.resolve", inc.ID, inc.FixPublicationID)
	return inc, nil
}

// MigrateAssignment moves a class task to another publication. Without confirm it
// only previews the impact. The old assignment keeps its attempts in the
// original context and becomes "migrated"; the new one starts clean.
func (s *Service) MigrateAssignment(tx *gorm.DB, a Actor, assignmentID, toPubID, reason string, confirm bool) (gin.H, error) {
	reason = strings.TrimSpace(reason)
	if toPubID == "" || reason == "" || len([]rune(reason)) > 400 {
		return nil, appErr(400, "VALIDATION_FAILED", "publicationId and reason 1..400 chars are required", "fix_input")
	}
	var old model.EduAssignment
	if tx.First(&old, "id = ? AND tenant_id = ?", assignmentID, a.TenantID).Error != nil {
		return nil, ErrNotFound
	}
	k, err := classForStaff(tx, a, old.ClassID)
	if err != nil {
		return nil, err
	}
	if old.State != "active" && old.State != "suspended" {
		return nil, appErr(409, "REVISION_CONFLICT", "only an active or suspended assignment can be migrated", "reload")
	}
	var pub model.EduPublication
	if tx.First(&pub, "id = ? AND tenant_id = ? AND state = 'published'", toPubID, a.TenantID).Error != nil || pub.ID == old.PublicationID {
		return nil, appErr(409, "REVISION_CONFLICT", "target must be a different published lesson", "publish_first")
	}
	var l model.EduLessonRevision
	var job model.EduMathJob
	if tx.First(&l, "id = ? AND tenant_id = ?", pub.LessonRevisionID, a.TenantID).Error != nil ||
		tx.First(&job, "id = ? AND tenant_id = ?", l.JobID, a.TenantID).Error != nil || job.WorkspaceID != k.WorkspaceID {
		return nil, ErrNotFound
	}
	if err := rightsAllow(tx, a.TenantID, l.ProblemRevisionID, "student"); err != nil {
		return nil, err
	}
	var students, attempts int64
	tx.Model(&model.EduAttempt{}).Where("tenant_id = ? AND assignment_id = ?", a.TenantID, old.ID).Count(&attempts)
	tx.Model(&model.EduAttempt{}).Where("tenant_id = ? AND assignment_id = ?", a.TenantID, old.ID).Distinct("student_user_id").Count(&students)
	var inc model.EduContentIncident
	tx.Where("tenant_id = ? AND publication_id = ?", a.TenantID, old.PublicationID).Order("created_at desc").Limit(1).Find(&inc)
	preview := gin.H{"fromAssignmentId": old.ID, "fromPublicationId": old.PublicationID, "toPublicationId": pub.ID, "classId": k.ID,
		"affectedStudents": students, "attemptsKept": attempts, "incidentId": inc.ID, "variantSetDropped": old.VariantSetID != "",
		"note": "old attempts stay with the old assignment in their original context; no automatic regrading"}
	if !confirm {
		preview["confirmed"] = false
		return preview, nil
	}
	if tx.Dialector.Name() == "postgres" {
		tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "edu-migrate:"+old.ID)
	}
	nu := model.EduAssignment{ID: flow.NewID("eas_"), TenantID: a.TenantID, ClassID: old.ClassID, PublicationID: pub.ID, Title: old.Title, State: "active",
		CreatedBy: a.UserID, CreatedAt: s.now(), StartsAt: old.StartsAt, DueAt: old.DueAt, AllowLate: old.AllowLate, AnswerPolicy: old.AnswerPolicy,
		ApproxAllowed: old.ApproxAllowed, UnitPolicy: old.UnitPolicy}
	m := model.EduAssignmentMigration{ID: flow.NewID("eam_"), TenantID: a.TenantID, FromAssignmentID: old.ID, ToAssignmentID: nu.ID, FromPublication: old.PublicationID,
		ToPublication: pub.ID, IncidentID: inc.ID, Reason: reason, AffectedStudents: int(students), AttemptsKept: int(attempts), CreatedBy: a.UserID, CreatedAt: s.now()}
	res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&m)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, appErr(409, "REVISION_CONFLICT", "assignment already migrated", "reload")
	}
	if err := tx.Create(&nu).Error; err != nil {
		return nil, err
	}
	up := tx.Model(&model.EduAssignment{}).Where("id = ? AND state IN ?", old.ID, []string{"active", "suspended"}).Update("state", "migrated")
	if up.Error != nil {
		return nil, up.Error
	}
	if up.RowsAffected == 0 {
		return nil, errors.New("assignment changed during migration")
	}
	s.audit(tx, a, "assignment.migrate", old.ID, nu.ID+" pub="+pub.ID)
	preview["confirmed"], preview["toAssignmentId"], preview["migrationId"] = true, nu.ID, m.ID
	return preview, nil
}

func (h *API) registerIncidents(g *gin.RouterGroup) {
	g.POST("/publications/:id/incidents", h.openIncident)
	g.GET("/incidents", h.listIncidents)
	g.GET("/incidents/:id", h.getIncident)
	g.POST("/incidents/:id/resolve", h.resolveIncident)
	g.POST("/assignments/:id/migrate", h.migrateAssignment)
	g.GET("/assignments/:id/migrations", h.listMigrations)
}

func (h *API) openIncident(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var in struct {
		Severity string `json:"severity"`
		Reason   string `json:"reason"`
	}
	if c.ShouldBindJSON(&in) != nil {
		h.fail(c, appErr(400, "VALIDATION_FAILED", "invalid body", "fix_input"))
		return
	}
	inc, created, err := h.Svc.OpenIncident(tx, a, c.Param("id"), in.Severity, in.Reason)
	if err != nil {
		h.fail(c, err)
		return
	}
	code := 200
	if created {
		code = 201
	}
	reply(c, code, incidentOut(inc), "ok")
}

func (h *API) listIncidents(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	q := tx.Where("tenant_id = ? AND workspace_id = ?", a.TenantID, a.WorkspaceID)
	if st := c.Query("state"); st == "open" || st == "resolved" {
		q = q.Where("state = ?", st)
	}
	if p := c.Query("publicationId"); p != "" {
		q = q.Where("publication_id = ?", p)
	}
	var out []model.EduContentIncident
	q.Order("created_at desc").Limit(200).Find(&out)
	reply(c, 200, out, "ok")
}

func (h *API) getIncident(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var inc model.EduContentIncident
	if tx.First(&inc, "id = ? AND tenant_id = ? AND workspace_id = ?", c.Param("id"), a.TenantID, a.WorkspaceID).Error != nil {
		h.fail(c, ErrNotFound)
		return
	}
	reply(c, 200, incidentOut(inc), "ok")
}

func (h *API) resolveIncident(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var in struct {
		FixPublicationID string `json:"fixPublicationId"`
		Note             string `json:"note"`
	}
	if c.ShouldBindJSON(&in) != nil {
		h.fail(c, appErr(400, "VALIDATION_FAILED", "invalid body", "fix_input"))
		return
	}
	inc, err := h.Svc.ResolveIncident(tx, a, c.Param("id"), in.FixPublicationID, in.Note)
	if err != nil {
		h.fail(c, err)
		return
	}
	reply(c, 200, incidentOut(inc), "ok")
}

func (h *API) migrateAssignment(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var in struct {
		PublicationID string `json:"publicationId"`
		Reason        string `json:"reason"`
		Confirm       bool   `json:"confirm"`
	}
	if c.ShouldBindJSON(&in) != nil {
		h.fail(c, appErr(400, "VALIDATION_FAILED", "invalid body", "fix_input"))
		return
	}
	out, err := h.Svc.MigrateAssignment(tx, a, c.Param("id"), in.PublicationID, in.Reason, in.Confirm)
	if err != nil {
		h.fail(c, err)
		return
	}
	code := 200
	if in.Confirm {
		code = 201
	}
	reply(c, code, out, "ok")
}

func (h *API) listMigrations(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var as model.EduAssignment
	if tx.First(&as, "id = ? AND tenant_id = ?", c.Param("id"), a.TenantID).Error != nil {
		h.fail(c, ErrNotFound)
		return
	}
	if _, err := classForStaff(tx, a, as.ClassID); err != nil {
		h.fail(c, err)
		return
	}
	var out []model.EduAssignmentMigration
	tx.Where("tenant_id = ? AND (from_assignment_id = ? OR to_assignment_id = ?)", a.TenantID, as.ID, as.ID).Order("created_at").Find(&out)
	reply(c, 200, out, "ok")
}

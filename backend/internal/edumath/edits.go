package edumath

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// Structured lesson edits and partial regeneration (FR-T05, FR-T06, AC-17, AC-18).
//
// A teacher edits single fields of an exact lesson revision (beat title, narration, board lines, hint
// text, term pronunciations), never HTML or code. The edit runs as math job kind "lesson_edit", which
// re-solves the problem, applies the edits to the base lesson artifact (bound by its SHA-256), re-runs
// the presentation consistency check and regenerates only the student projection and the previews. The
// problem, solution, verification, figure and check question are kept. The result is a NEW lesson
// revision whose parent is the base; the base and any publication of it stay unchanged.
//
// Concurrency (AC-17): each base revision has one head. A normal save takes the head; a second save of
// the same base, or a save against a stale hash, is refused with REVISION_CONFLICT, the conflicting edit
// and the caller's own edits echoed back, so nothing typed is lost. "Save as branch" creates a sibling
// revision instead. A head whose job failed or was cancelled is released.
//
// Review (constraint 3): an edited revision is ready_for_review. The only exception is the institution
// rule editReviewInherit = pronunciation_only (default none; pending user decision, see doc 07): a
// pronunciation-only edit of an approved revision inherits that approval through a review record marked
// InheritedFrom, never as a new human review.

const (
	maxLessonEdits   = 50
	editStageMaxMS   = 10000 + 5000 + 30000 // apply_edits + project_student + render_preview timeouts
	inheritNone      = "none"
	inheritPronounce = "pronunciation_only"
)

var editImpact = map[string][]string{
	"reasoning":     {"review", "student_projection", "preview", "media"},
	"wording":       {"review", "student_projection", "preview", "media"},
	"pronunciation": {"media", "captions"},
}

type lessonEditInput struct {
	Target  string          `json:"target"`
	ID      string          `json:"id,omitempty"`
	Field   string          `json:"field,omitempty"`
	Level   int             `json:"level,omitempty"`
	Value   json.RawMessage `json:"value,omitempty"`
	Term    string          `json:"term,omitempty"`
	Reading *string         `json:"reading,omitempty"`
}

// EditImpact is the dry run shown before the teacher saves (AC-18).
type EditImpact struct {
	Classes     []string `json:"classes"`
	Invalidates []string `json:"invalidates"`
	Keeps       []string `json:"keeps"`
	Regenerates []string `json:"regenerates"`
	ManualCheck bool     `json:"manualCheck"`
}

// EditConflict is returned when the head of a base revision is taken or the base hash is stale.
type EditConflict struct {
	Reason           string               `json:"reason"` // stale_hash | head_taken
	LatestRevisionID string               `json:"latestRevisionId,omitempty"`
	Edit             *model.EduLessonEdit `json:"edit,omitempty"`
}

func (e *EditConflict) Error() string { return "REVISION_CONFLICT: " + e.Reason }

func classifyEdit(e lessonEditInput) (string, error) {
	switch {
	case e.Target == "beat" && e.Field == "board":
		return "reasoning", nil
	case e.Target == "beat" && (e.Field == "title" || e.Field == "say"):
		return "wording", nil
	case e.Target == "hint" && e.Field == "text":
		return "wording", nil
	case e.Target == "pronunciation":
		return "pronunciation", nil
	}
	return "", appErr(403, "POLICY_REVOKED", "this part is not editable here; change the problem revision instead", "new_problem_revision")
}

// ImpactOf classifies an edit list without touching any lesson (mirrors mathsvc/edits.impact).
func ImpactOf(edits []lessonEditInput) (EditImpact, error) {
	if len(edits) == 0 || len(edits) > maxLessonEdits {
		return EditImpact{}, appErr(400, "INPUT_AMBIGUOUS", fmt.Sprintf("edits must be a list of 1..%d items", maxLessonEdits), "")
	}
	cls, inv := map[string]bool{}, map[string]bool{}
	for _, e := range edits {
		c, err := classifyEdit(e)
		if err != nil {
			return EditImpact{}, err
		}
		cls[c] = true
		for _, x := range editImpact[c] {
			inv[x] = true
		}
	}
	out := EditImpact{Keeps: []string{"problem", "solution", "verification", "figure", "check_question"},
		Regenerates: []string{"apply_edits", "project_student", "render_preview"}, ManualCheck: cls["reasoning"]}
	for c := range cls {
		out.Classes = append(out.Classes, c)
	}
	for x := range inv {
		out.Invalidates = append(out.Invalidates, x)
	}
	sort.Strings(out.Classes)
	sort.Strings(out.Invalidates)
	return out, nil
}

// EditReviewRule returns the institution's edit review inheritance rule (default none).
func (s *Service) EditReviewRule(tx *gorm.DB, tenantID string) string {
	var p model.EduOrgPolicy
	if tx.First(&p, "tenant_id = ?", tenantID).Error == nil && p.EditReviewInherit == inheritPronounce {
		return inheritPronounce
	}
	return inheritNone
}

func inheritsApproval(rule string, classes []string, parentState string) bool {
	return rule == inheritPronounce && len(classes) == 1 && classes[0] == "pronunciation" && parentState == "approved"
}

func editEstimate(pb flow.PriceBook) gin.H {
	unit := unitPrice(pb)
	maxCost := int64(editStageMaxMS) * unit
	if b := JobBudgetMicro(); maxCost > b {
		maxCost = b
	}
	return gin.H{"usageType": flow.UsageMathComputeMS, "priceVersion": pb.Version, "unitMicro": unit, "maxComputeMs": editStageMaxMS,
		"maxCostMicro": maxCost, "reservationMicro": JobBudgetMicro(), "note": "upper bound from stage timeouts; settled on measured compute only"}
}

func parseEdits(raw json.RawMessage) ([]lessonEditInput, string, error) {
	var edits []lessonEditInput
	if len(raw) == 0 || json.Unmarshal(raw, &edits) != nil {
		return nil, "", appErr(400, "INPUT_AMBIGUOUS", "edits must be a JSON list", "")
	}
	canon, _ := json.Marshal(edits)
	return edits, string(canon), nil
}

func (s *Service) editBase(ctx context.Context, tx *gorm.DB, a Actor, lessonID string) (model.EduLessonRevision, model.EduProblemRevision, model.EduSolutionRevision, model.EduMathJob, model.EduArtifact, error) {
	var l model.EduLessonRevision
	var sol model.EduSolutionRevision
	var src model.EduMathJob
	var art model.EduArtifact
	if err := tx.First(&l, "id = ? AND tenant_id = ?", lessonID, a.TenantID).Error; err != nil {
		return l, model.EduProblemRevision{}, sol, src, art, ErrNotFound
	}
	r, _, err := s.revision(tx, a, l.ProblemRevisionID)
	if err != nil {
		return l, r, sol, src, art, err
	}
	if tx.First(&sol, "id = ? AND tenant_id = ?", l.SolutionRevisionID, a.TenantID).Error != nil ||
		tx.First(&src, "id = ? AND tenant_id = ?", sol.JobID, a.TenantID).Error != nil ||
		tx.First(&art, "id = ? AND tenant_id = ?", l.LessonArtifact, a.TenantID).Error != nil {
		return l, r, sol, src, art, ErrNotFound
	}
	return l, r, sol, src, art, nil
}

// StartLessonEdit validates, takes the head (or a branch) and dispatches the lesson_edit job.
func (s *Service) StartLessonEdit(ctx context.Context, tx *gorm.DB, a Actor, lessonID, baseHash string, rawEdits json.RawMessage, branch bool, idemKey string) (model.EduMathJob, model.EduLessonEdit, error) {
	if !a.Has("teacher") {
		return model.EduMathJob{}, model.EduLessonEdit{}, ErrNoRole
	}
	edits, canon, err := parseEdits(rawEdits)
	if err != nil {
		return model.EduMathJob{}, model.EduLessonEdit{}, err
	}
	imp, err := ImpactOf(edits)
	if err != nil {
		return model.EduMathJob{}, model.EduLessonEdit{}, err
	}
	l, r, sol, src, art, err := s.editBase(ctx, tx, a, lessonID)
	if err != nil {
		return model.EduMathJob{}, model.EduLessonEdit{}, err
	}
	if sol.MathState != "verified" || strings.Contains(l.BlockingErrors, "math_not_verified") {
		return model.EduMathJob{}, model.EduLessonEdit{}, appErr(409, "VALIDATION_FAILED", "the solution is not verified; editing wording cannot fix it", "teacher_review")
	}
	inputHash := hashOf("lesson_edit", l.ID, l.LessonHash, canon, fmt.Sprint(branch))
	var prior model.EduMathJob
	if idemKey != "" && tx.First(&prior, "tenant_id = ? AND kind = ? AND idem_key = ?", a.TenantID, "lesson_edit", idemKey).Error == nil {
		if prior.InputHash != inputHash {
			return prior, model.EduLessonEdit{}, appErr(409, "REVISION_CONFLICT", "idempotency key reused with a different input", "use_new_key")
		}
		var ed model.EduLessonEdit
		tx.First(&ed, "job_id = ?", prior.ID)
		return prior, ed, nil // replay: same job, same edit
	}
	if baseHash != l.LessonHash {
		return model.EduMathJob{}, model.EduLessonEdit{}, &EditConflict{Reason: "stale_hash", LatestRevisionID: s.latestHead(tx, l.ID)}
	}
	now := s.now()
	ed := model.EduLessonEdit{ID: flow.NewID("ele_"), TenantID: a.TenantID, BaseLessonRevisionID: l.ID, BaseLessonHash: l.LessonHash, Branch: branch,
		EditsJSON: canon, Classes: strings.Join(imp.Classes, ","), State: "running", CreatedBy: a.UserID, CreatedAt: now}
	ed.JobID = "pending:" + ed.ID
	ed.HeadKey = "head:" + l.ID
	if branch {
		ed.HeadKey = "branch:" + ed.ID
	} else if err := s.releaseDeadHead(tx, ed.HeadKey); err != nil {
		return model.EduMathJob{}, ed, err
	}
	ins := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&ed)
	if ins.Error != nil {
		return model.EduMathJob{}, ed, ins.Error
	}
	if ins.RowsAffected == 0 {
		var holder model.EduLessonEdit
		tx.First(&holder, "head_key = ?", ed.HeadKey)
		return model.EduMathJob{}, ed, &EditConflict{Reason: "head_taken", LatestRevisionID: s.latestHead(tx, l.ID), Edit: &holder}
	}
	var problem map[string]any
	_ = json.Unmarshal([]byte(r.Structured), &problem)
	var editList []any
	_ = json.Unmarshal([]byte(canon), &editList)
	payload := map[string]any{"problem": problem, "problem_hash": r.InputHash, "analysis_job_id": src.ServiceJobID, "solution_hash": sol.SolutionHash,
		"report_hash": sol.ReportHash, "base_lesson_artifact_id": art.ServiceArtifactID, "base_lesson_hash": art.SHA256, "edits": editList}
	opts, _ := json.Marshal(map[string]any{"editId": ed.ID})
	job, err := s.startJobWith(ctx, tx, a, "lesson_edit", idemKey, inputHash, r.ID, src.ID, string(opts), payload)
	if err != nil {
		s.releaseEdit(tx, ed.ID, "failed")
		return job, ed, err
	}
	tx.Model(&ed).Update("job_id", job.ID)
	_ = tx.First(&ed, "id = ?", ed.ID)
	if job.Status == "failed" || job.Status == "cancelled" {
		s.releaseEdit(tx, ed.ID, "failed")
		ed.State = "failed"
	}
	s.audit(tx, a, "lesson.edit", ed.ID, l.ID+" "+ed.Classes)
	return job, ed, nil
}

// releaseDeadHead frees a head whose edit job ended without a revision.
func (s *Service) releaseDeadHead(tx *gorm.DB, headKey string) error {
	var holder model.EduLessonEdit
	if tx.First(&holder, "head_key = ?", headKey).Error != nil {
		return nil
	}
	if holder.State == "failed" {
		return tx.Model(&holder).Update("head_key", "released:"+holder.ID).Error
	}
	var job model.EduMathJob
	if holder.State == "running" && tx.First(&job, "id = ?", holder.JobID).Error == nil && (job.Status == "failed" || job.Status == "cancelled") {
		return tx.Model(&holder).Updates(map[string]any{"head_key": "released:" + holder.ID, "state": "failed"}).Error
	}
	return nil
}

func (s *Service) releaseEdit(tx *gorm.DB, editID, state string) {
	tx.Model(&model.EduLessonEdit{}).Where("id = ? AND state = ?", editID, "running").Updates(map[string]any{"head_key": "released:" + editID, "state": state})
}

// latestHead follows the non-branch edit chain from a revision to its newest descendant.
func (s *Service) latestHead(tx *gorm.DB, lessonID string) string {
	cur := lessonID
	for i := 0; i < 100; i++ {
		var ed model.EduLessonEdit
		if tx.First(&ed, "head_key = ? AND state = ?", "head:"+cur, "done").Error != nil || ed.ResultLessonRevisionID == "" {
			return cur
		}
		cur = ed.ResultLessonRevisionID
	}
	return cur
}

// editOf finds the edit of a lesson_edit job through the job options (set before dispatch, so it
// also works when the service answers synchronously).
func (s *Service) editOf(tx *gorm.DB, job model.EduMathJob) (model.EduLessonEdit, error) {
	var opt struct {
		EditID string `json:"editId"`
	}
	var ed model.EduLessonEdit
	if json.Unmarshal([]byte(job.Options), &opt) != nil || opt.EditID == "" {
		return ed, fmt.Errorf("%w: lesson_edit job options", ErrContract)
	}
	if err := tx.First(&ed, "id = ? AND tenant_id = ?", opt.EditID, job.TenantID).Error; err != nil {
		return ed, err
	}
	return ed, nil
}

func (s *Service) editPayload(tx *gorm.DB, job model.EduMathJob, payload map[string]any) error {
	var base model.EduLessonRevision
	var art model.EduArtifact
	ed, err := s.editOf(tx, job)
	if err != nil {
		return err
	}
	if tx.First(&base, "id = ?", ed.BaseLessonRevisionID).Error != nil ||
		tx.First(&art, "id = ?", base.LessonArtifact).Error != nil {
		return ErrNotFound
	}
	var list []any
	_ = json.Unmarshal([]byte(ed.EditsJSON), &list)
	payload["base_lesson_artifact_id"], payload["base_lesson_hash"], payload["edits"] = art.ServiceArtifactID, art.SHA256, list
	return nil
}

// materializeEdit creates the child revision; approval is inherited only under the explicit institution rule.
func (s *Service) materializeEdit(tx *gorm.DB, job model.EduMathJob, arts map[string]model.EduArtifact) error {
	var n int64
	tx.Model(&model.EduLessonRevision{}).Where("job_id = ?", job.ID).Count(&n)
	if n > 0 {
		return nil
	}
	la := arts["lesson"]
	if la.ID == "" || arts["lesson_student"].ID == "" || arts["preview_html"].ID == "" || arts["preview_html_student"].ID == "" {
		return fmt.Errorf("%w: missing edited lesson artifacts", ErrContract)
	}
	var base model.EduLessonRevision
	ed, err := s.editOf(tx, job)
	if err != nil {
		return err
	}
	if err := tx.First(&base, "id = ? AND tenant_id = ?", ed.BaseLessonRevisionID, job.TenantID).Error; err != nil {
		return err
	}
	classes := strings.Split(ed.Classes, ",")
	child := model.EduLessonRevision{ID: flow.NewID("elr_"), TenantID: job.TenantID, ProblemRevisionID: base.ProblemRevisionID,
		SolutionRevisionID: base.SolutionRevisionID, JobID: job.ID, LessonArtifact: la.ID, LessonHash: la.SHA256, StudentArtifact: arts["lesson_student"].ID,
		PreviewArtifact: arts["preview_html"].ID, StudentPreview: arts["preview_html_student"].ID, State: "ready_for_review", CreatedBy: job.CreatedBy,
		CreatedAt: s.now(), ParentLessonRevisionID: base.ID, EditClasses: ed.Classes}
	var parentRec model.EduReviewRecord
	inherit := inheritsApproval(s.EditReviewRule(tx, job.TenantID), classes, base.State) &&
		tx.Where("lesson_revision_id = ? AND lesson_hash = ? AND decision = ?", base.ID, base.LessonHash, "approved").Order("created_at desc").First(&parentRec).Error == nil
	if inherit {
		child.State, child.ApprovalInheritedFrom = "approved", base.ID
	}
	if err := tx.Create(&child).Error; err != nil {
		return err
	}
	if inherit {
		rec := model.EduReviewRecord{ID: flow.NewID("err_"), TenantID: job.TenantID, LessonRevisionID: child.ID, LessonHash: child.LessonHash,
			ReviewerID: parentRec.ReviewerID, SelfReview: parentRec.SelfReview, Decision: "approved", InheritedFrom: parentRec.ID,
			Comment: "approval inherited: pronunciation only edit under institution rule pronunciation_only", CreatedAt: s.now()}
		if err := tx.Create(&rec).Error; err != nil {
			return err
		}
	}
	return tx.Model(&model.EduLessonEdit{}).Where("id = ?", ed.ID).Updates(map[string]any{"state": "done", "result_lesson_revision_id": child.ID}).Error
}

// ---------------------------------------------------------------------------
// HTTP

func (h *API) registerEdits(g *gin.RouterGroup) {
	g.GET("/lesson-revisions/:id/editable", h.getEditable)
	g.POST("/lesson-revisions/:id/edit-impact", h.editImpact)
	g.POST("/lesson-revisions/:id/edits", h.startEdit)
	g.GET("/lesson-revisions/:id/history", h.lessonHistory)
}

// getEditable returns the editable fields of the exact revision (teacher view; answer stays in teacher scope).
func (h *API) getEditable(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	ctx, cancel := ctx15(c)
	defer cancel()
	l, _, _, _, art, err := h.Svc.editBase(ctx, tx, a, c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	raw, _, err := h.Svc.Client.Artifact(ctx, a.TenantID, art)
	if err != nil {
		h.fail(c, fmt.Errorf("%w: lesson artifact", ErrContract))
		return
	}
	var doc struct {
		Beats []struct {
			ID     string   `json:"id"`
			Title  string   `json:"title"`
			Say    string   `json:"say"`
			Board  []string `json:"board"`
			Reveal bool     `json:"reveals_answer"`
		} `json:"beats"`
		Hints []struct {
			Level int    `json:"level"`
			Kind  string `json:"kind"`
			Text  string `json:"text"`
		} `json:"hints"`
		Pronunciations map[string]string `json:"pronunciations"`
		Quality        json.RawMessage   `json:"quality"`
		Meta           struct {
			EditLog json.RawMessage `json:"edit_log"`
		} `json:"meta"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		h.fail(c, fmt.Errorf("%w: lesson document", ErrContract))
		return
	}
	reply(c, 200, gin.H{"lessonRevisionId": l.ID, "lessonHash": l.LessonHash, "state": l.State, "parentLessonRevisionId": l.ParentLessonRevisionID,
		"beats": doc.Beats, "hints": doc.Hints, "pronunciations": doc.Pronunciations, "quality": doc.Quality, "editLog": doc.Meta.EditLog,
		"editReviewInherit": h.Svc.EditReviewRule(tx, a.TenantID), "latestRevisionId": h.Svc.latestHead(tx, l.ID),
		"limits": gin.H{"maxEdits": maxLessonEdits, "maxText": 600, "maxBoardLines": 12, "maxLine": 200}}, "ok")
}

func (h *API) editImpact(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	l, ok := h.staffLesson(c, tx, a, c.Param("id"))
	if !ok {
		return
	}
	var in struct {
		Edits json.RawMessage `json:"edits"`
	}
	_ = c.ShouldBindJSON(&in)
	edits, _, err := parseEdits(in.Edits)
	if err == nil {
		var imp EditImpact
		if imp, err = ImpactOf(edits); err == nil {
			rule := h.Svc.EditReviewRule(tx, a.TenantID)
			reply(c, 200, gin.H{"impact": imp, "estimate": editEstimate(flow.CurrentPriceBook()), "editReviewInherit": rule,
				"approvalInherited": inheritsApproval(rule, imp.Classes, l.State), "baseState": l.State}, "ok")
			return
		}
	}
	h.fail(c, err)
}

func (h *API) startEdit(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var in struct {
		BaseLessonHash string          `json:"baseLessonHash"`
		Edits          json.RawMessage `json:"edits"`
		SaveAs         string          `json:"saveAs"`
		IdempotencyKey string          `json:"idempotencyKey"`
	}
	if c.ShouldBindJSON(&in) != nil {
		reply(c, 400, nil, "invalid body")
		return
	}
	key := c.GetHeader("Idempotency-Key")
	if key == "" {
		key = in.IdempotencyKey
	}
	ctx, cancel := ctx15(c)
	defer cancel()
	j, ed, err := h.Svc.StartLessonEdit(ctx, tx, a, c.Param("id"), in.BaseLessonHash, in.Edits, in.SaveAs == "branch", key)
	var ce *EditConflict
	if errors.As(err, &ce) {
		c.JSON(http.StatusConflict, gin.H{"code": 409, "msg": "revision conflict", "data": gin.H{
			"error":    gin.H{"code": "REVISION_CONFLICT", "message": "the lesson changed or another edit holds its head", "next_action": "reload_or_save_as_branch"},
			"conflict": ce, "yourEdits": in.Edits}})
		return
	}
	if err != nil {
		h.fail(c, err)
		return
	}
	reply(c, 202, gin.H{"job": jobOut(j), "edit": ed}, "accepted")
}

// lessonHistory returns the lineage (ancestors) and the edits made on this revision.
func (h *API) lessonHistory(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	l, ok := h.staffLesson(c, tx, a, c.Param("id"))
	if !ok {
		return
	}
	type node struct {
		ID                    string    `json:"id"`
		LessonHash            string    `json:"lessonHash"`
		State                 string    `json:"state"`
		EditClasses           string    `json:"editClasses,omitempty"`
		ApprovalInheritedFrom string    `json:"approvalInheritedFrom,omitempty"`
		Parent                string    `json:"parentLessonRevisionId,omitempty"`
		CreatedBy             string    `json:"createdBy"`
		CreatedAt             time.Time `json:"createdAt"`
	}
	var chain []node
	cur := l
	for i := 0; i < 100; i++ {
		chain = append(chain, node{cur.ID, cur.LessonHash, cur.State, cur.EditClasses, cur.ApprovalInheritedFrom, cur.ParentLessonRevisionID, cur.CreatedBy, cur.CreatedAt})
		var parent model.EduLessonRevision // fresh value: gorm would add the old primary key to the query
		if cur.ParentLessonRevisionID == "" || tx.First(&parent, "id = ? AND tenant_id = ?", cur.ParentLessonRevisionID, a.TenantID).Error != nil {
			break
		}
		cur = parent
	}
	var edits []model.EduLessonEdit
	tx.Where("base_lesson_revision_id = ? AND tenant_id = ?", l.ID, a.TenantID).Order("created_at").Find(&edits)
	reply(c, 200, gin.H{"lineage": chain, "edits": edits, "latestRevisionId": h.Svc.latestHead(tx, l.ID)}, "ok")
}

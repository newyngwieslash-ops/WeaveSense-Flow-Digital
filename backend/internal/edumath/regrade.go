package edumath

import (
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// Regrading (DEV-025). A teacher re-evaluates the main answers of one
// assignment against a reference publication: by default the migration target
// when the task was migrated, otherwise the assignment's own publication. It
// previews first; on confirm only changed results are written, each with old
// and new result and the reason, and the attempt keeps its assignment and
// response (original context). A grader failure never turns into "incorrect":
// the attempt is skipped and reported. Students get a notice without the
// teacher's reason text, so no answer can leak through it (constraint 2).

const maxRegradeAttempts = 300

func (h *API) registerRegrade(g *gin.RouterGroup) {
	g.POST("/assignments/:id/regrade", h.regrade)
	g.GET("/assignments/:id/regrades", h.listRegrades)
	g.GET("/student/regrades", h.studentRegrades)
	g.POST("/student/regrades/:id/seen", h.studentRegradeSeen)
}

func (h *API) regradeRef(tx *gorm.DB, a Actor, as model.EduAssignment, k model.EduClass, want string) (model.EduPublication, error) {
	var pub model.EduPublication
	if want == "" {
		var m model.EduAssignmentMigration
		if tx.First(&m, "tenant_id = ? AND from_assignment_id = ?", a.TenantID, as.ID).Error == nil {
			want = m.ToPublication
		} else {
			want = as.PublicationID
		}
	}
	if tx.First(&pub, "id = ? AND tenant_id = ? AND state = 'published'", want, a.TenantID).Error != nil {
		return pub, appErr(409, "REVISION_CONFLICT", "reference must be a published lesson (withdrawn content cannot be a reference)", "publish_fix")
	}
	var own, ref model.EduPublication
	var l model.EduLessonRevision
	var job model.EduMathJob
	tx.First(&own, "id = ? AND tenant_id = ?", as.PublicationID, a.TenantID)
	tx.First(&ref, "id = ?", pub.ID)
	if tx.First(&l, "id = ? AND tenant_id = ?", pub.LessonRevisionID, a.TenantID).Error != nil ||
		tx.First(&job, "id = ? AND tenant_id = ?", l.JobID, a.TenantID).Error != nil || job.WorkspaceID != k.WorkspaceID {
		return pub, ErrNotFound
	}
	if ref.LessonRevisionID != own.LessonRevisionID && !descendsFrom(tx, a.TenantID, ref.LessonRevisionID, own.LessonRevisionID) {
		return pub, appErr(409, "VALIDATION_FAILED", "reference must be the assignment's lesson or an edit of it", "edit_revision")
	}
	return pub, nil
}

func (h *API) regrade(c *gin.Context) {
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
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Reason == "" || len([]rune(in.Reason)) > 200 {
		h.fail(c, appErr(400, "VALIDATION_FAILED", "reason 1..200 chars is required", "fix_input"))
		return
	}
	var as model.EduAssignment
	if tx.First(&as, "id = ? AND tenant_id = ?", c.Param("id"), a.TenantID).Error != nil {
		h.fail(c, ErrNotFound)
		return
	}
	k, err := classForStaff(tx, a, as.ClassID)
	if err != nil {
		h.fail(c, err)
		return
	}
	pub, err := h.regradeRef(tx, a, as, k, in.PublicationID)
	if err != nil {
		h.fail(c, err)
		return
	}
	var ats []model.EduAttempt
	tx.Where("tenant_id = ? AND assignment_id = ? AND item = 'main' AND kind = 'answer' AND valid = ?", a.TenantID, as.ID, true).
		Order("created_at").Limit(maxRegradeAttempts + 1).Find(&ats)
	if len(ats) > maxRegradeAttempts {
		h.fail(c, appErr(409, "PARAMETER_LIMIT", "too many attempts for one regrade request", "contact_support"))
		return
	}
	lesson, err := h.lessonFor(c, tx, a.TenantID, pub)
	if err != nil {
		h.fail(c, err)
		return
	}
	exp := map[string]any{"exact": lesson.Answer.Exact, "plain": lesson.Answer.Plain}
	type change struct {
		at       model.EduAttempt
		newRes   string
		grader   string
		newRsn   string
		teacherd bool
	}
	var changes []change
	var skipped []string
	unchanged := 0
	students := map[string]bool{}
	for _, at := range ats {
		ctx, cancel := ctx15(c)
		g, gerr := h.Svc.Client.Grade(ctx, a.TenantID, map[string]any{"expected": exp, "response": at.Response,
			"approx": map[string]any{"allowed": as.ApproxAllowed, "rel_tol": 1e-3}, "unit_policy": as.UnitPolicy})
		cancel()
		if gerr != nil || g.Result == "" || g.Result == "invalid" {
			skipped = append(skipped, at.ID)
			continue
		}
		if g.Result == at.Result {
			unchanged++
			continue
		}
		changes = append(changes, change{at: at, newRes: g.Result, grader: g.Grader, newRsn: g.Reason, teacherd: at.TeacherResult != ""})
		students[at.StudentUserID] = true
	}
	list := make([]gin.H, 0, len(changes))
	for _, ch := range changes {
		list = append(list, gin.H{"attemptId": ch.at.ID, "studentUserId": ch.at.StudentUserID, "oldResult": ch.at.Result, "newResult": ch.newRes, "teacherJudged": ch.teacherd})
	}
	out := gin.H{"assignmentId": as.ID, "refPublicationId": pub.ID, "examined": len(ats), "changed": list, "unchanged": unchanged, "skipped": skipped,
		"affectedStudents": len(students), "confirmed": false}
	if !in.Confirm {
		reply(c, 200, out, "ok")
		return
	}
	batch := flow.NewID("erg_")
	written := 0
	for _, ch := range changes {
		r := model.EduRegrade{ID: flow.NewID("erd_"), TenantID: a.TenantID, BatchID: batch, AssignmentID: as.ID, AttemptID: ch.at.ID, RefPublicationID: pub.ID,
			StudentUserID: ch.at.StudentUserID, OldResult: ch.at.Result, NewResult: ch.newRes, Grader: ch.grader, Reason: in.Reason, CreatedBy: a.UserID, CreatedAt: h.Svc.now()}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&r)
		if res.Error != nil {
			h.fail(c, res.Error)
			return
		}
		if res.RowsAffected == 0 {
			continue // already regraded against this reference
		}
		upd := tx.Model(&model.EduAttempt{}).Where("id = ? AND result = ?", ch.at.ID, ch.at.Result).
			Updates(map[string]any{"result": ch.newRes, "reason": "regraded", "grader": ch.grader})
		if upd.Error != nil {
			h.fail(c, upd.Error)
			return
		}
		written++
	}
	h.Svc.audit(tx, a, "assignment.regrade", as.ID, batch+" ref="+pub.ID)
	out["confirmed"], out["batchId"], out["written"] = true, batch, written
	reply(c, 201, out, "ok")
}

func (h *API) listRegrades(c *gin.Context) {
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
	var out []model.EduRegrade
	tx.Where("tenant_id = ? AND assignment_id = ?", a.TenantID, as.ID).Order("created_at").Limit(1000).Find(&out)
	reply(c, 200, out, "ok")
}

// studentRegrades lists the caller's own regrade notices; the teacher's reason is
// deliberately not included.
func (h *API) studentRegrades(c *gin.Context) {
	a, tx, ok := h.student(c)
	if !ok {
		return
	}
	var rs []model.EduRegrade
	tx.Where("tenant_id = ? AND student_user_id = ?", a.TenantID, a.UserID).Order("created_at desc").Limit(200).Find(&rs)
	out := make([]gin.H, 0, len(rs))
	for _, r := range rs {
		var as model.EduAssignment
		tx.Select("id", "title").First(&as, "id = ? AND tenant_id = ?", r.AssignmentID, a.TenantID)
		out = append(out, gin.H{"id": r.ID, "assignmentId": r.AssignmentID, "assignmentTitle": as.Title, "oldResult": r.OldResult, "newResult": r.NewResult,
			"note": "老师修正内容后重新评分", "createdAt": r.CreatedAt, "seen": r.NoticeSeenAt != nil})
	}
	reply(c, 200, out, "ok")
}

func (h *API) studentRegradeSeen(c *gin.Context) {
	a, tx, ok := h.student(c)
	if !ok {
		return
	}
	now := h.Svc.now()
	res := tx.Model(&model.EduRegrade{}).Where("id = ? AND tenant_id = ? AND student_user_id = ?", c.Param("id"), a.TenantID, a.UserID).
		Where("notice_seen_at IS NULL").Update("notice_seen_at", now)
	if res.Error != nil {
		h.fail(c, res.Error)
		return
	}
	var n int64
	tx.Model(&model.EduRegrade{}).Where("id = ? AND tenant_id = ? AND student_user_id = ?", c.Param("id"), a.TenantID, a.UserID).Count(&n)
	if n == 0 {
		h.fail(c, ErrNotFound)
		return
	}
	reply(c, 200, gin.H{"seen": true}, "ok")
}

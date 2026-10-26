package edumath

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// Course configuration (DEV-014). Staff of the workspace manage courses with
// optimistic versioning; classes point at a course. Lesson alignment to course
// knowledge points is "textbookAligned" only after a teacher confirms it.

var courseNotations = map[string]bool{"cn_textbook": true, "latex_standard": true}
var courseLanguages = map[string]bool{"zh-CN": true, "en": true}

type courseIn struct {
	Name            string   `json:"name"`
	Grade           string   `json:"grade"`
	Textbook        string   `json:"textbook"`
	TextbookEdition string   `json:"textbookEdition"`
	KnowledgePoints []string `json:"knowledgePoints"`
	AllowedMethods  []string `json:"allowedMethods"`
	Notation        string   `json:"notation"`
	Language        string   `json:"language"`
	RetainRevisions int      `json:"retainRevisions"`
	Version         int      `json:"version"`
	Reason          string   `json:"reason"`
}

func cleanList(in []string, max, maxLen int) ([]string, bool) {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || len([]rune(v)) > maxLen {
			return nil, false
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out, len(out) <= max
}

func (in *courseIn) validate() (kps, methods []string, err error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Textbook, in.TextbookEdition, in.Grade = strings.TrimSpace(in.Textbook), strings.TrimSpace(in.TextbookEdition), strings.TrimSpace(in.Grade)
	if in.Notation == "" {
		in.Notation = "cn_textbook"
	}
	if in.Language == "" {
		in.Language = "zh-CN"
	}
	if in.RetainRevisions == 0 {
		in.RetainRevisions = 50
	}
	var ok1, ok2 bool
	kps, ok1 = cleanList(in.KnowledgePoints, 300, 60)
	methods, ok2 = cleanList(in.AllowedMethods, 50, 60)
	bad := in.Name == "" || len([]rune(in.Name)) > 120 || len([]rune(in.Textbook)) > 120 || len([]rune(in.TextbookEdition)) > 60 || len([]rune(in.Grade)) > 40 ||
		!courseNotations[in.Notation] || !courseLanguages[in.Language] || in.RetainRevisions < 1 || in.RetainRevisions > 500 || !ok1 || !ok2
	if bad {
		return nil, nil, appErr(400, "VALIDATION_FAILED", "name 1..120; notation cn_textbook|latex_standard; language zh-CN|en; retainRevisions 1..500; at most 300 knowledge points and 50 methods, each 1..60 chars", "fix_input")
	}
	return kps, methods, nil
}

func jsonList(s string) []string {
	out := []string{}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func mustList(v []string) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func courseView(k model.EduCourse) gin.H {
	return gin.H{"id": k.ID, "workspaceId": k.WorkspaceID, "name": k.Name, "grade": k.Grade, "textbook": k.Textbook, "textbookEdition": k.TextbookEdition,
		"knowledgePoints": jsonList(k.KnowledgePoints), "allowedMethods": jsonList(k.AllowedMethods), "notation": k.Notation, "language": k.Language,
		"retainRevisions": k.RetainRevisions, "version": k.Version, "status": k.Status, "updatedBy": k.UpdatedBy, "updatedAt": k.UpdatedAt}
}

func alignView(al model.EduCourseAlignment) gin.H {
	return gin.H{"id": al.ID, "courseId": al.CourseID, "lessonRevisionId": al.LessonRevisionID, "knowledgePoints": jsonList(al.KnowledgePoints),
		"state": al.State, "note": al.Note, "suggestedBy": al.SuggestedBy, "confirmedBy": al.ConfirmedBy, "confirmedAt": al.ConfirmedAt,
		"textbookAligned": al.State == "confirmed"}
}

func (h *API) registerCourses(g *gin.RouterGroup) {
	g.POST("/courses", h.createCourse)
	g.GET("/courses", h.listCourses)
	g.GET("/courses/:id", h.getCourse)
	g.PUT("/courses/:id", h.updateCourse)
	g.PUT("/classes/:id/course", h.setClassCourse)
	g.POST("/courses/:id/alignments", h.proposeAlignment)
	g.POST("/course-alignments/:id/confirm", h.decideAlignment("confirmed"))
	g.POST("/course-alignments/:id/reject", h.decideAlignment("rejected"))
	g.GET("/lesson-revisions/:id/alignments", h.lessonAlignments)
}

func courseForStaff(tx *gorm.DB, a Actor, id string) (model.EduCourse, error) {
	var k model.EduCourse
	if tx.First(&k, "id = ? AND tenant_id = ? AND workspace_id = ? AND status = 'active'", id, a.TenantID, a.WorkspaceID).Error != nil {
		return k, ErrNotFound
	}
	return k, nil
}

func lessonInWorkspace(tx *gorm.DB, a Actor, id string) (model.EduLessonRevision, error) {
	var l model.EduLessonRevision
	var job model.EduMathJob
	if tx.First(&l, "id = ? AND tenant_id = ?", id, a.TenantID).Error != nil ||
		tx.First(&job, "id = ? AND tenant_id = ?", l.JobID, a.TenantID).Error != nil || job.WorkspaceID != a.WorkspaceID {
		return l, ErrNotFound
	}
	return l, nil
}

func (h *API) createCourse(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var in courseIn
	if c.ShouldBindJSON(&in) != nil {
		h.fail(c, appErr(400, "VALIDATION_FAILED", "invalid body", "fix_input"))
		return
	}
	kps, methods, err := in.validate()
	if err != nil {
		h.fail(c, err)
		return
	}
	now := h.Svc.now()
	k := model.EduCourse{ID: flow.NewID("ecs_"), TenantID: a.TenantID, WorkspaceID: a.WorkspaceID, Name: in.Name, Grade: in.Grade, Textbook: in.Textbook,
		TextbookEdition: in.TextbookEdition, KnowledgePoints: mustList(kps), AllowedMethods: mustList(methods), Notation: in.Notation, Language: in.Language,
		RetainRevisions: in.RetainRevisions, Version: 1, Status: "active", CreatedBy: a.UserID, UpdatedBy: a.UserID, CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&k).Error; err != nil {
		h.fail(c, err)
		return
	}
	h.Svc.audit(tx, a, "course.create", k.ID, k.Name)
	reply(c, 201, courseView(k), "ok")
}

func (h *API) listCourses(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var ks []model.EduCourse
	tx.Where("tenant_id = ? AND workspace_id = ? AND status = 'active'", a.TenantID, a.WorkspaceID).Order("created_at").Limit(500).Find(&ks)
	out := make([]gin.H, 0, len(ks))
	for _, k := range ks {
		out = append(out, courseView(k))
	}
	reply(c, 200, out, "ok")
}

func (h *API) getCourse(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	k, err := courseForStaff(tx, a, c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	var als []model.EduCourseAlignment
	tx.Where("tenant_id = ? AND course_id = ?", a.TenantID, k.ID).Order("created_at").Limit(1000).Find(&als)
	list := make([]gin.H, 0, len(als))
	for _, al := range als {
		list = append(list, alignView(al))
	}
	var classes []string
	tx.Model(&model.EduClass{}).Where("tenant_id = ? AND course_id = ? AND status = 'active'", a.TenantID, k.ID).Pluck("id", &classes)
	reply(c, 200, gin.H{"course": courseView(k), "alignments": list, "classes": classes}, "ok")
}

func (h *API) updateCourse(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	k, err := courseForStaff(tx, a, c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	var in courseIn
	if c.ShouldBindJSON(&in) != nil {
		h.fail(c, appErr(400, "VALIDATION_FAILED", "invalid body", "fix_input"))
		return
	}
	kps, methods, err := in.validate()
	if err != nil {
		h.fail(c, err)
		return
	}
	if in.Version != k.Version {
		h.fail(c, appErr(409, "VERSION_CONFLICT", "the course changed since you loaded it", "reload"))
		return
	}
	textbookChanged := in.Textbook != k.Textbook || in.TextbookEdition != k.TextbookEdition || mustList(kps) != k.KnowledgePoints
	res := tx.Model(&model.EduCourse{}).Where("id = ? AND version = ?", k.ID, k.Version).Updates(map[string]any{
		"name": in.Name, "grade": in.Grade, "textbook": in.Textbook, "textbook_edition": in.TextbookEdition, "knowledge_points": mustList(kps),
		"allowed_methods": mustList(methods), "notation": in.Notation, "language": in.Language, "retain_revisions": in.RetainRevisions,
		"version": k.Version + 1, "updated_by": a.UserID, "updated_at": h.Svc.now()})
	if res.Error != nil {
		h.fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		h.fail(c, appErr(409, "VERSION_CONFLICT", "the course changed since you loaded it", "reload"))
		return
	}
	var reopened int64
	if textbookChanged {
		r := tx.Model(&model.EduCourseAlignment{}).Where("tenant_id = ? AND course_id = ? AND state = 'confirmed'", a.TenantID, k.ID).
			Updates(map[string]any{"state": "suggested", "note": "course_changed", "confirmed_by": "", "confirmed_at": nil, "updated_at": h.Svc.now()})
		reopened = r.RowsAffected
	}
	h.Svc.audit(tx, a, "course.update", k.ID, strings.TrimSpace(in.Reason))
	tx.First(&k, "id = ?", k.ID)
	reply(c, 200, gin.H{"course": courseView(k), "alignmentsNeedReconfirm": reopened}, "ok")
}

func (h *API) setClassCourse(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	k, err := classForStaff(tx, a, c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	var in struct {
		CourseID string `json:"courseId"`
	}
	_ = c.ShouldBindJSON(&in)
	if in.CourseID != "" {
		if _, err := courseForStaff(tx, a, in.CourseID); err != nil {
			h.fail(c, err)
			return
		}
	}
	if err := tx.Model(&model.EduClass{}).Where("id = ?", k.ID).Update("course_id", in.CourseID).Error; err != nil {
		h.fail(c, err)
		return
	}
	h.Svc.audit(tx, a, "class.course", k.ID, in.CourseID)
	reply(c, 200, gin.H{"classId": k.ID, "courseId": in.CourseID}, "ok")
}

// proposeAlignment records knowledge points for a lesson revision. confirm=true
// is the teacher's own confirmation; otherwise it is a suggestion that is not
// shown as aligned. Points must belong to the course.
func (h *API) proposeAlignment(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	k, err := courseForStaff(tx, a, c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	var in struct {
		LessonRevisionID string   `json:"lessonRevisionId"`
		KnowledgePoints  []string `json:"knowledgePoints"`
		Confirm          bool     `json:"confirm"`
		SuggestedBy      string   `json:"suggestedBy"`
	}
	if c.ShouldBindJSON(&in) != nil {
		h.fail(c, appErr(400, "VALIDATION_FAILED", "invalid body", "fix_input"))
		return
	}
	l, err := lessonInWorkspace(tx, a, in.LessonRevisionID)
	if err != nil {
		h.fail(c, err)
		return
	}
	kps, okl := cleanList(in.KnowledgePoints, 50, 60)
	allowed := map[string]bool{}
	for _, p := range jsonList(k.KnowledgePoints) {
		allowed[p] = true
	}
	for _, p := range kps {
		okl = okl && allowed[p]
	}
	if !okl || len(kps) == 0 {
		h.fail(c, appErr(400, "VALIDATION_FAILED", "knowledgePoints must be 1..50 points of this course", "fix_input"))
		return
	}
	now := h.Svc.now()
	by := strings.TrimSpace(in.SuggestedBy)
	if by == "" || len(by) > 60 {
		by = "user:" + a.UserID
	}
	var al model.EduCourseAlignment
	if tx.First(&al, "tenant_id = ? AND course_id = ? AND lesson_revision_id = ?", a.TenantID, k.ID, l.ID).Error != nil {
		al = model.EduCourseAlignment{ID: flow.NewID("eca_"), TenantID: a.TenantID, CourseID: k.ID, LessonRevisionID: l.ID, CreatedAt: now}
	}
	al.KnowledgePoints, al.SuggestedBy, al.UpdatedAt, al.Note = mustList(kps), by, now, ""
	al.State, al.ConfirmedBy, al.ConfirmedAt = "suggested", "", nil
	if in.Confirm {
		al.State, al.ConfirmedBy, al.ConfirmedAt = "confirmed", a.UserID, &now
	}
	if err := tx.Save(&al).Error; err != nil {
		h.fail(c, err)
		return
	}
	h.Svc.audit(tx, a, "course.align."+al.State, al.ID, l.ID)
	reply(c, 201, alignView(al), "ok")
}

func (h *API) decideAlignment(state string) gin.HandlerFunc {
	return func(c *gin.Context) {
		a, tx, ok := h.staff(c)
		if !ok {
			return
		}
		var al model.EduCourseAlignment
		if tx.First(&al, "id = ? AND tenant_id = ?", c.Param("id"), a.TenantID).Error != nil {
			h.fail(c, ErrNotFound)
			return
		}
		if _, err := courseForStaff(tx, a, al.CourseID); err != nil {
			h.fail(c, err)
			return
		}
		now := h.Svc.now()
		upd := map[string]any{"state": state, "updated_at": now, "note": ""}
		if state == "confirmed" {
			upd["confirmed_by"], upd["confirmed_at"] = a.UserID, &now
		} else {
			upd["confirmed_by"], upd["confirmed_at"] = "", (*time.Time)(nil)
		}
		if err := tx.Model(&model.EduCourseAlignment{}).Where("id = ?", al.ID).Updates(upd).Error; err != nil {
			h.fail(c, err)
			return
		}
		h.Svc.audit(tx, a, "course.align."+state, al.ID, al.LessonRevisionID)
		tx.First(&al, "id = ?", al.ID)
		reply(c, 200, alignView(al), "ok")
	}
}

func (h *API) lessonAlignments(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	l, err := lessonInWorkspace(tx, a, c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	var als []model.EduCourseAlignment
	tx.Where("tenant_id = ? AND lesson_revision_id = ?", a.TenantID, l.ID).Order("created_at").Find(&als)
	out := make([]gin.H, 0, len(als))
	for _, al := range als {
		out = append(out, alignView(al))
	}
	reply(c, 200, out, "ok")
}

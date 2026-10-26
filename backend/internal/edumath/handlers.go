package edumath

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// API wires the workbench routes.
type API struct {
	Svc  *Service
	DB   *gorm.DB
	User flow.UserFunc
}

// Register mounts /api/edu/* on the given group (expects the group at /api).
func (h *API) Register(api *gin.RouterGroup) {
	api.POST("/edu-join", h.join)             // outside the tenant-scoped middleware (see join)
	api.POST("/edu-math-events", h.mathEvent) // service-signed push; no user session (see events.go)
	g := api.Group("/edu", Middleware(h.DB, h.User))
	h.registerClasses(g)
	h.registerLedger(g)
	g.GET("/me", h.me)
	g.POST("/roles", h.bindRole)
	g.POST("/problems", h.createProblem)
	g.POST("/problems/:id/revisions", h.reviseProblem)
	g.POST("/problem-revisions/:id/confirm", h.confirm)
	g.POST("/problem-revisions/:id/analyze", h.analyze)
	g.GET("/jobs/:id", h.getJob)
	g.POST("/jobs/:id/cancel", h.cancelJob)
	g.GET("/jobs/:id/events", h.jobEvents)
	g.POST("/jobs/:id/reconcile", h.reconcileJob)
	g.GET("/solution-revisions/:id", h.getSolution)
	g.POST("/solution-revisions/:id/lesson", h.buildLesson)
	g.GET("/lesson-revisions/:id", h.getLesson)
	g.POST("/lesson-revisions/:id/review", h.review)
	g.POST("/lesson-revisions/:id/publish", h.publish)
	g.GET("/artifacts/:id/url", h.artifactURL)
	g.GET("/student/assignments", h.studentAssignments)
	g.GET("/student/assignments/:id", h.studentAssignment)
	g.GET("/files/:id", h.file)
	h.registerLearning(g)
	h.registerHelp(g)
	h.registerFigure(g)
	h.registerVariants(g)
	h.registerEdits(g)
	h.registerAdminLists(g)
}

func (h *API) fail(c *gin.Context, err error) {
	var ae *AppError
	switch {
	case errors.As(err, &ae):
		c.JSON(ae.Status, gin.H{"code": ae.Status, "data": gin.H{"error": gin.H{"code": ae.Code, "message": ae.Message, "next_action": ae.NextAction}}, "msg": ae.Message})
	case errors.Is(err, ErrConsent):
		c.JSON(http.StatusForbidden, gin.H{"code": 403, "data": gin.H{"error": gin.H{"code": "CONSENT_REQUIRED", "message": "guardian consent is missing or withdrawn", "next_action": "contact_institution"}}, "msg": "consent required"})
	case errors.Is(err, ErrNotFound):
		reply(c, http.StatusNotFound, nil, "not found")
	case errors.Is(err, ErrNoRole), errors.Is(err, ErrForbidden):
		reply(c, http.StatusForbidden, nil, "forbidden")
	case errors.Is(err, ErrContract), errors.Is(err, ErrServiceSent):
		reply(c, http.StatusBadGateway, gin.H{"error": gin.H{"code": "PROVIDER_UNAVAILABLE"}}, "math service error")
	default:
		reply(c, http.StatusInternalServerError, nil, "internal error")
	}
}

func (h *API) staff(c *gin.Context) (Actor, *gorm.DB, bool) {
	u, _ := c.Get("edumath.user")
	user, _ := u.(*model.User)
	tx := reqDB(c, h.DB)
	if user == nil {
		reply(c, http.StatusUnauthorized, nil, "login required")
		return Actor{}, tx, false
	}
	a, err := StaffActor(tx, user.ID, c.GetHeader("X-Workspace-Id"))
	if err != nil {
		h.fail(c, err)
		return a, tx, false
	}
	return a, tx, true
}

func (h *API) student(c *gin.Context) (Actor, *gorm.DB, bool) {
	u, _ := c.Get("edumath.user")
	user, _ := u.(*model.User)
	tx := reqDB(c, h.DB)
	if user == nil {
		reply(c, http.StatusUnauthorized, nil, "login required")
		return Actor{}, tx, false
	}
	a, err := StudentActor(tx, user.ID, c.GetHeader("X-Edu-Tenant"))
	if err != nil {
		h.fail(c, err)
		return a, tx, false
	}
	return a, tx, true
}

func ctx15(c *gin.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), 15*time.Second)
}

func (h *API) me(c *gin.Context) {
	u, _ := c.Get("edumath.user")
	user := u.(*model.User)
	var bs []model.EduRoleBinding
	reqDB(c, h.DB).Where("user_id = ? AND status = ?", user.ID, "active").Find(&bs)
	reply(c, 200, gin.H{"userId": user.ID, "roles": bs}, "ok")
}

// bindRole: tenant owner/admin or org_admin binds staff roles. Students join via
// class invitations (POST /api/edu-join), never through this endpoint.
func (h *API) bindRole(c *gin.Context) {
	u, _ := c.Get("edumath.user")
	user := u.(*model.User)
	var in struct {
		TenantID, UserID, Role string
	}
	if c.ShouldBindJSON(&in) != nil {
		reply(c, 400, nil, "invalid body")
		return
	}
	tx := reqDB(c, h.DB)
	if in.Role != "teacher" && in.Role != "reviewer" && in.Role != "org_admin" {
		reply(c, 400, nil, "role must be teacher, reviewer or org_admin")
		return
	}
	if !canManageRoles(tx, in.TenantID, user.ID) {
		reply(c, 403, nil, "forbidden")
		return
	}
	var m int64
	tx.Model(&model.Membership{}).Where("tenant_id = ? AND user_id = ? AND status = ?", in.TenantID, in.UserID, "active").Count(&m)
	if m == 0 && !(in.TenantID == model.PersonalTenantID(user.ID) && in.UserID == user.ID) {
		reply(c, 400, nil, "staff roles require tenant membership")
		return
	}
	b := model.EduRoleBinding{ID: flow.NewID("erb_"), TenantID: in.TenantID, UserID: in.UserID, Role: in.Role, Status: "active", CreatedBy: user.ID, CreatedAt: time.Now().UTC()}
	if err := tx.Where("tenant_id = ? AND user_id = ? AND role = ?", in.TenantID, in.UserID, in.Role).FirstOrCreate(&b).Error; err != nil {
		h.fail(c, err)
		return
	}
	h.Svc.audit(tx, Actor{UserID: user.ID, TenantID: in.TenantID}, "role.bind", in.UserID, in.Role)
	reply(c, 200, b, "ok")
}

func (h *API) createProblem(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var in struct {
		FamilyID   string          `json:"familyId"`
		Title      string          `json:"title"`
		Structured json.RawMessage `json:"structured"`
	}
	if c.ShouldBindJSON(&in) != nil {
		reply(c, 400, nil, "invalid body")
		return
	}
	p, r, err := h.Svc.CreateProblem(tx, a, in.FamilyID, in.Title, in.Structured)
	if err != nil {
		h.fail(c, err)
		return
	}
	reply(c, 201, gin.H{"problem": p, "revision": r}, "ok")
}

func (h *API) reviseProblem(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var in struct {
		BaseRevision int             `json:"baseRevision"`
		Structured   json.RawMessage `json:"structured"`
	}
	if c.ShouldBindJSON(&in) != nil {
		reply(c, 400, nil, "invalid body")
		return
	}
	r, err := h.Svc.ReviseProblem(tx, a, c.Param("id"), in.BaseRevision, in.Structured)
	if err != nil {
		h.fail(c, err)
		return
	}
	reply(c, 201, r, "ok")
}

func (h *API) confirm(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var in struct {
		InputHash string `json:"inputHash"`
	}
	_ = c.ShouldBindJSON(&in)
	r, err := h.Svc.ConfirmProblem(tx, a, c.Param("id"), in.InputHash)
	if err != nil {
		h.fail(c, err)
		return
	}
	reply(c, 200, r, "ok")
}

func idemKey(c *gin.Context) string {
	if k := c.GetHeader("Idempotency-Key"); k != "" {
		return k
	}
	var in struct {
		IdempotencyKey string `json:"idempotencyKey"`
	}
	_ = c.ShouldBindJSON(&in)
	return in.IdempotencyKey
}

func jobOut(j model.EduMathJob) gin.H {
	var e any
	if j.ErrorJSON != "" {
		_ = json.Unmarshal([]byte(j.ErrorJSON), &e)
	}
	return gin.H{"id": j.ID, "kind": j.Kind, "status": j.Status, "stage": j.Stage, "progress": j.Progress, "seq": j.Seq,
		"priceVersion": j.PriceVersion, "budgetMicro": j.BudgetMicro, "settlement": j.Settlement, "error": e, "cancelRequested": j.CancelRequested,
		"problemRevisionId": j.ProblemRevisionID, "createdAt": j.CreatedAt, "updatedAt": j.UpdatedAt}
}

func (h *API) analyze(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	ctx, cancel := ctx15(c)
	defer cancel()
	j, err := h.Svc.StartAnalysis(ctx, tx, a, c.Param("id"), idemKey(c))
	if err != nil {
		h.fail(c, err)
		return
	}
	reply(c, 202, jobOut(j), "accepted")
}

func (h *API) loadJob(c *gin.Context, tx *gorm.DB, a Actor) (model.EduMathJob, bool) {
	var j model.EduMathJob
	if err := tx.First(&j, "id = ? AND tenant_id = ? AND workspace_id = ?", c.Param("id"), a.TenantID, a.WorkspaceID).Error; err != nil {
		h.fail(c, ErrNotFound)
		return j, false
	}
	return j, true
}

func (h *API) getJob(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	j, ok := h.loadJob(c, tx, a)
	if !ok {
		return
	}
	ctx, cancel := ctx15(c)
	defer cancel()
	j, err := h.Svc.Sync(ctx, tx, j)
	if err != nil {
		h.fail(c, err)
		return
	}
	out := jobOut(j)
	var stages []model.EduStageRun
	tx.Where("job_id = ?", j.ID).Order("created_at").Find(&stages)
	out["stageRuns"] = stages
	// Artifact metadata only (no content, no links). For a job that did not
	// succeed these are partial results (DEV-054): kept for audit, never
	// materialized into revisions.
	var arts []model.EduArtifact
	tx.Where("job_id = ?", j.ID).Order("created_at").Find(&arts)
	al := make([]gin.H, 0, len(arts))
	for _, x := range arts {
		al = append(al, gin.H{"id": x.ID, "kind": x.Kind, "audience": x.Audience, "mediaType": x.MediaType, "sha256": x.SHA256, "size": x.Size,
			"partial": j.Status != "succeeded"})
	}
	out["artifacts"] = al
	var sol model.EduSolutionRevision
	if tx.First(&sol, "job_id = ?", j.ID).Error == nil {
		out["solutionRevisionId"] = sol.ID
	}
	var l model.EduLessonRevision
	if tx.First(&l, "job_id = ?", j.ID).Error == nil {
		out["lessonRevisionId"] = l.ID
	}
	var vs model.EduVariantSet
	if tx.First(&vs, "job_id = ?", j.ID).Error == nil {
		out["variantSetId"] = vs.ID
	}
	reply(c, 200, out, "ok")
}

func (h *API) cancelJob(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	j, ok := h.loadJob(c, tx, a)
	if !ok {
		return
	}
	ctx, cancel := ctx15(c)
	defer cancel()
	j, err := h.Svc.Cancel(ctx, tx, a, j)
	if err != nil {
		h.fail(c, err)
		return
	}
	reply(c, 200, jobOut(j), "ok")
}

func (h *API) reconcileJob(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	j, ok := h.loadJob(c, tx, a)
	if !ok {
		return
	}
	ctx, cancel := ctx15(c)
	defer cancel()
	j, err := h.Svc.Reconcile(ctx, tx, j)
	if err != nil {
		h.fail(c, err)
		return
	}
	reply(c, 200, jobOut(j), "ok")
}

func (h *API) getSolution(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var s model.EduSolutionRevision
	if err := tx.First(&s, "id = ? AND tenant_id = ?", c.Param("id"), a.TenantID).Error; err != nil {
		h.fail(c, ErrNotFound)
		return
	}
	if _, _, err := h.Svc.revision(tx, a, s.ProblemRevisionID); err != nil {
		h.fail(c, err)
		return
	}
	reply(c, 200, s, "ok")
}

func (h *API) buildLesson(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	ctx, cancel := ctx15(c)
	defer cancel()
	j, err := h.Svc.StartLessonBuild(ctx, tx, a, c.Param("id"), idemKey(c))
	if err != nil {
		h.fail(c, err)
		return
	}
	reply(c, 202, jobOut(j), "accepted")
}

func (h *API) getLesson(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var l model.EduLessonRevision
	if err := tx.First(&l, "id = ? AND tenant_id = ?", c.Param("id"), a.TenantID).Error; err != nil {
		h.fail(c, ErrNotFound)
		return
	}
	if _, _, err := h.Svc.revision(tx, a, l.ProblemRevisionID); err != nil {
		h.fail(c, err)
		return
	}
	var reviews []model.EduReviewRecord
	tx.Where("lesson_revision_id = ?", l.ID).Order("created_at").Find(&reviews)
	var pub model.EduPublication
	out := gin.H{"lesson": l, "reviews": reviews, "reviewMode": h.Svc.policy(tx, a.TenantID)}
	if tx.First(&pub, "lesson_revision_id = ?", l.ID).Error == nil {
		out["publication"] = pub
	}
	reply(c, 200, out, "ok")
}

func (h *API) review(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var in struct {
		LessonHash string `json:"lessonHash"`
		Decision   string `json:"decision"`
		Comment    string `json:"comment"`
	}
	_ = c.ShouldBindJSON(&in)
	r, err := h.Svc.Review(tx, a, c.Param("id"), in.LessonHash, in.Decision, in.Comment)
	if err != nil {
		h.fail(c, err)
		return
	}
	reply(c, 200, r, "ok")
}

func (h *API) publish(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var in struct {
		LessonHash string `json:"lessonHash"`
	}
	_ = c.ShouldBindJSON(&in)
	p, err := h.Svc.Publish(tx, a, c.Param("id"), in.LessonHash)
	if err != nil {
		h.fail(c, err)
		return
	}
	reply(c, 200, p, "ok")
}

func (h *API) artifactURL(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var art model.EduArtifact
	if err := tx.First(&art, "id = ? AND tenant_id = ?", c.Param("id"), a.TenantID).Error; err != nil || !canFetch(tx, art, a.UserID) {
		h.fail(c, ErrNotFound)
		return
	}
	u, exp := h.Svc.SignedURL(art.ID, a.UserID, MaxURLTTL)
	reply(c, 200, gin.H{"url": u, "expiresAt": exp, "audience": art.Audience, "kind": art.Kind, "sha256": art.SHA256}, "ok")
}

func (h *API) studentAssignments(c *gin.Context) {
	a, tx, ok := h.student(c)
	if !ok {
		return
	}
	if !consentOK(tx, a.TenantID, a.UserID) {
		h.fail(c, ErrConsent)
		return
	}
	var classIDs []string
	tx.Model(&model.EduEnrollment{}).Where("tenant_id = ? AND student_user_id = ? AND status = ?", a.TenantID, a.UserID, "active").Pluck("class_id", &classIDs)
	var as []model.EduAssignment
	if len(classIDs) > 0 {
		tx.Where("tenant_id = ? AND class_id IN ? AND state = ?", a.TenantID, classIDs, "active").Order("created_at desc").Find(&as)
	}
	out := []gin.H{}
	for _, x := range as {
		if _, _, err := studentCanAccessAssignment(tx, a.TenantID, a.UserID, x.ID); err == nil {
			out = append(out, gin.H{"id": x.ID, "title": x.Title, "classId": x.ClassID, "createdAt": x.CreatedAt})
		}
	}
	reply(c, 200, out, "ok")
}

// studentAssignment returns the server-side student projection (never the teacher lesson).
func (h *API) studentAssignment(c *gin.Context) {
	a, tx, ok := h.student(c)
	if !ok {
		return
	}
	as, pub, err := studentCanAccessAssignment(tx, a.TenantID, a.UserID, c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	var l model.EduLessonRevision
	if err := tx.First(&l, "id = ? AND tenant_id = ?", pub.LessonRevisionID, a.TenantID).Error; err != nil {
		h.fail(c, ErrNotFound)
		return
	}
	var sa, ta, sp model.EduArtifact
	if tx.First(&sa, "id = ? AND audience = ?", l.StudentArtifact, "student").Error != nil ||
		tx.First(&ta, "id = ?", l.LessonArtifact).Error != nil || tx.First(&sp, "id = ? AND audience = ?", l.StudentPreview, "student").Error != nil {
		h.fail(c, ErrNotFound)
		return
	}
	ctx, cancel := ctx15(c)
	defer cancel()
	raw, _, err := h.Svc.Client.Artifact(ctx, a.TenantID, sa)
	if err != nil {
		h.fail(c, err)
		return
	}
	teacher, _, err := h.Svc.Client.Artifact(ctx, a.TenantID, ta)
	if err != nil {
		h.fail(c, err)
		return
	}
	var proj map[string]any
	if json.Unmarshal(raw, &proj) != nil || StudentLeak(proj, teacher) {
		reply(c, 500, nil, "student projection rejected by answer guard")
		return
	}
	u, exp := h.Svc.SignedURL(sp.ID, a.UserID, MaxURLTTL)
	reply(c, 200, gin.H{"assignment": gin.H{"id": as.ID, "title": as.Title}, "lesson": proj, "preview": gin.H{"url": u, "expiresAt": exp}}, "ok")
}

// StudentLeak is the platform-side second guard: the projection must be the
// student/none variant, every explanation beat must be locked, there must be no
// answer or answer key, and no answer marker of the teacher lesson may appear
// in any string. A marker that already occurs in the public givens (problem
// statement, given parameters, self-test prompt) carries no answer information
// and is skipped, otherwise short integer answers would block every lesson.
func StudentLeak(proj map[string]any, teacherLesson []byte) bool {
	if proj["projection"] != "student" || proj["reveal"] != "none" {
		return true
	}
	for _, k := range []string{"answer", "check_answer_key"} {
		if _, ok := proj[k]; ok {
			return true
		}
	}
	if beats, ok := proj["beats"].([]any); ok {
		for _, b := range beats {
			m, _ := b.(map[string]any)
			if m == nil || m["locked"] != true || m["board"] != nil || m["say"] != nil {
				return true
			}
		}
	}
	var t struct {
		Answer         map[string]any `json:"answer"`
		CheckAnswerKey map[string]any `json:"check_answer_key"`
		Problem        struct {
			Text   string            `json:"text"`
			Params map[string]string `json:"params"`
		} `json:"problem"`
		CheckQuestion struct {
			Prompt string            `json:"prompt"`
			Params map[string]string `json:"params"`
		} `json:"check_question"`
	}
	if json.Unmarshal(teacherLesson, &t) != nil {
		return true
	}
	public := []string{t.Problem.Text, t.CheckQuestion.Prompt}
	for _, v := range t.Problem.Params {
		public = append(public, v)
	}
	for _, v := range t.CheckQuestion.Params {
		public = append(public, v)
	}
	isPublic := func(m string) bool {
		for _, g := range public {
			if g != "" && strings.Contains(g, m) {
				return true
			}
		}
		return false
	}
	raw, _ := json.Marshal(proj)
	var plain any
	_ = json.Unmarshal(raw, &plain)
	texts := []string{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			texts = append(texts, x)
		case map[string]any:
			for k, vv := range x {
				texts = append(texts, k)
				walk(vv)
			}
		case []any:
			for _, vv := range x {
				walk(vv)
			}
		}
	}
	walk(plain)
	for _, src := range []map[string]any{t.Answer, t.CheckAnswerKey} {
		for _, k := range []string{"plain", "exact", "latex"} {
			m, _ := src[k].(string)
			if m == "" || isPublic(m) {
				continue
			}
			for _, s := range texts {
				if strings.Contains(s, m) {
					return true
				}
			}
		}
	}
	return false
}

// file serves artifact bytes for a signed URL, re-checking access each time.
func (h *API) file(c *gin.Context) {
	id, uid := c.Param("id"), c.Query("u")
	if !h.Svc.VerifyURL(id, uid, c.Query("exp"), c.Query("sig")) {
		reply(c, 403, nil, "invalid or expired link")
		return
	}
	var art model.EduArtifact
	if err := h.DB.First(&art, "id = ?", id).Error; err != nil || !canFetch(h.DB, art, uid) {
		reply(c, 403, nil, "access revoked")
		return
	}
	ctx, cancel := ctx15(c)
	defer cancel()
	data, ct, err := h.Svc.Client.Artifact(ctx, art.TenantID, art)
	if err != nil {
		h.fail(c, err)
		return
	}
	hd := c.Writer.Header()
	hd.Set("Content-Security-Policy", "sandbox allow-same-origin; default-src 'none'; img-src data:; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Cache-Control", "private, no-store")
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("X-Artifact-Sha256", art.SHA256)
	if ct == "" {
		ct = art.MediaType
	}
	c.Data(200, ct, data)
}

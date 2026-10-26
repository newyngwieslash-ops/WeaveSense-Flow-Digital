package edumath

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// Source rights (DEV-026, FR-D01). A problem may point at a SourceAsset rights
// record. Its flags are checked where the content leaves the teacher's desk:
// assigning to students and every student read (allowStudent), variant
// generation (allowDerivative). Expiry and withdrawal are evaluated at request
// time, so access stops without any background job. A problem with no record
// is treated as teacher original and stays unrestricted; the dependents view
// lists every lesson, publication and assignment that relies on a record.

var sourceKinds = map[string]bool{"teacher_original": true, "textbook": true, "exam": true, "publisher": true, "web": true, "other": true}

// rightsFor returns the rights record behind a lesson revision, if any.
func rightsFor(tx *gorm.DB, tenant, problemRevisionID string) (model.EduSourceAsset, bool) {
	var src model.EduSourceAsset
	var pr model.EduProblemRevision
	var p model.EduProblem
	if tx.Select("id", "problem_id").First(&pr, "id = ? AND tenant_id = ?", problemRevisionID, tenant).Error != nil ||
		tx.Select("id", "source_asset_id").First(&p, "id = ? AND tenant_id = ?", pr.ProblemID, tenant).Error != nil || p.SourceAssetID == "" {
		return src, false
	}
	if tx.First(&src, "id = ? AND tenant_id = ?", p.SourceAssetID, tenant).Error != nil {
		return src, true // a dangling link fails closed
	}
	return src, true
}

func rightsLive(src model.EduSourceAsset, now time.Time) bool {
	return src.ID != "" && src.State == "active" && (src.ExpiresAt == nil || src.ExpiresAt.After(now))
}

// rightsAllow reports whether use is permitted for the lesson's problem.
// use is "student" or "derivative".
func rightsAllow(tx *gorm.DB, tenant, problemRevisionID, use string) error {
	src, linked := rightsFor(tx, tenant, problemRevisionID)
	if !linked {
		return nil
	}
	ok := rightsLive(src, time.Now().UTC())
	switch use {
	case "student":
		ok = ok && src.AllowStudent
	case "derivative":
		ok = ok && src.AllowDerivative
	default:
		ok = false
	}
	if !ok {
		return appErr(409, "RIGHTS_RESTRICTED", "the source rights do not allow this use (expired, withdrawn or not granted)", "check_source_rights")
	}
	return nil
}

func (h *API) registerSources(g *gin.RouterGroup) {
	g.POST("/source-assets", h.createSource)
	g.GET("/source-assets", h.listSources)
	g.GET("/source-assets/:id", h.getSource)
	g.PUT("/source-assets/:id/rights", h.updateSourceRights)
	g.POST("/source-assets/:id/withdraw", h.withdrawSource)
	g.PUT("/problems/:id/source", h.linkProblemSource)
}

type sourceIn struct {
	Title           string     `json:"title"`
	Kind            string     `json:"kind"`
	Attribution     string     `json:"attribution"`
	LicenseRef      string     `json:"licenseRef"`
	AllowStudent    bool       `json:"allowStudent"`
	AllowDerivative bool       `json:"allowDerivative"`
	AllowExport     bool       `json:"allowExport"`
	AllowCrossOrg   bool       `json:"allowCrossOrg"`
	ExpiresAt       *time.Time `json:"expiresAt"`
	Reason          string     `json:"reason"`
}

func (in *sourceIn) valid(create bool) bool {
	in.Title, in.Attribution, in.LicenseRef, in.Reason = strings.TrimSpace(in.Title), strings.TrimSpace(in.Attribution), strings.TrimSpace(in.LicenseRef), strings.TrimSpace(in.Reason)
	if create && (in.Title == "" || len([]rune(in.Title)) > 200 || !sourceKinds[in.Kind]) {
		return false
	}
	if len([]rune(in.Attribution)) > 200 || len([]rune(in.LicenseRef)) > 200 || len([]rune(in.Reason)) > 200 {
		return false
	}
	if create && in.Kind != "teacher_original" && in.Attribution == "" {
		return false // third party material must carry attribution
	}
	if in.ExpiresAt != nil {
		e := in.ExpiresAt.UTC()
		if !e.After(time.Now().UTC()) {
			return false
		}
		in.ExpiresAt = &e
	}
	return true
}

func (h *API) teacherOrAdmin(c *gin.Context) (Actor, *gorm.DB, bool) {
	a, tx, ok := h.staff(c)
	if !ok {
		return a, tx, false
	}
	if !a.Has("teacher") && !canManageRoles(tx, a.TenantID, a.UserID) {
		h.fail(c, ErrNoRole)
		return a, tx, false
	}
	return a, tx, true
}

func (h *API) createSource(c *gin.Context) {
	a, tx, ok := h.teacherOrAdmin(c)
	if !ok {
		return
	}
	var in sourceIn
	if c.ShouldBindJSON(&in) != nil || !in.valid(true) {
		h.fail(c, appErr(400, "VALIDATION_FAILED", "title 1..200, known kind, attribution for third party material, future expiresAt", "fix_input"))
		return
	}
	now := h.Svc.now()
	src := model.EduSourceAsset{ID: flow.NewID("esa_"), TenantID: a.TenantID, WorkspaceID: a.WorkspaceID, Title: in.Title, Kind: in.Kind, Attribution: in.Attribution,
		LicenseRef: in.LicenseRef, AllowStudent: in.AllowStudent, AllowDerivative: in.AllowDerivative, AllowExport: in.AllowExport, AllowCrossOrg: in.AllowCrossOrg,
		ExpiresAt: in.ExpiresAt, State: "active", CreatedBy: a.UserID, CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&src).Error; err != nil {
		h.fail(c, err)
		return
	}
	h.Svc.audit(tx, a, "source.create", src.ID, src.Kind)
	reply(c, 201, src, "ok")
}

func (h *API) loadSource(c *gin.Context, tx *gorm.DB, a Actor) (model.EduSourceAsset, bool) {
	var src model.EduSourceAsset
	if tx.First(&src, "id = ? AND tenant_id = ? AND workspace_id = ?", c.Param("id"), a.TenantID, a.WorkspaceID).Error != nil {
		h.fail(c, ErrNotFound)
		return src, false
	}
	return src, true
}

func sourceDependents(tx *gorm.DB, tenant, srcID string) gin.H {
	var problems []string
	tx.Model(&model.EduProblem{}).Where("tenant_id = ? AND source_asset_id = ?", tenant, srcID).Order("id").Pluck("id", &problems)
	out := gin.H{"problems": problems, "lessonRevisions": []string{}, "publications": []string{}, "activeAssignments": []string{}, "variantSets": []string{}}
	if len(problems) == 0 {
		return out
	}
	var revs, lessons, pubs, as, sets []string
	tx.Model(&model.EduProblemRevision{}).Where("tenant_id = ? AND problem_id IN ?", tenant, problems).Pluck("id", &revs)
	if len(revs) > 0 {
		tx.Model(&model.EduLessonRevision{}).Where("tenant_id = ? AND problem_revision_id IN ?", tenant, revs).Order("created_at").Pluck("id", &lessons)
	}
	if len(lessons) > 0 {
		tx.Model(&model.EduPublication{}).Where("tenant_id = ? AND lesson_revision_id IN ?", tenant, lessons).Order("created_at").Pluck("id", &pubs)
		tx.Model(&model.EduVariantSet{}).Where("tenant_id = ? AND lesson_revision_id IN ?", tenant, lessons).Order("created_at").Pluck("id", &sets)
	}
	if len(pubs) > 0 {
		tx.Model(&model.EduAssignment{}).Where("tenant_id = ? AND publication_id IN ? AND state = 'active'", tenant, pubs).Order("created_at").Pluck("id", &as)
	}
	for k, v := range map[string][]string{"lessonRevisions": lessons, "publications": pubs, "activeAssignments": as, "variantSets": sets} {
		if v != nil {
			out[k] = v
		}
	}
	return out
}

func (h *API) listSources(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var out []model.EduSourceAsset
	tx.Where("tenant_id = ? AND workspace_id = ?", a.TenantID, a.WorkspaceID).Order("created_at desc").Limit(500).Find(&out)
	reply(c, 200, out, "ok")
}

func (h *API) getSource(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	src, ok := h.loadSource(c, tx, a)
	if !ok {
		return
	}
	reply(c, 200, gin.H{"source": src, "live": rightsLive(src, time.Now().UTC()), "dependents": sourceDependents(tx, a.TenantID, src.ID)}, "ok")
}

// updateSourceRights changes the rights flags and expiry; the change is audited
// with a reason and applies to every dependent at the next request.
func (h *API) updateSourceRights(c *gin.Context) {
	a, tx, ok := h.teacherOrAdmin(c)
	if !ok {
		return
	}
	src, ok := h.loadSource(c, tx, a)
	if !ok {
		return
	}
	var in sourceIn
	if c.ShouldBindJSON(&in) != nil || !in.valid(false) || in.Reason == "" {
		h.fail(c, appErr(400, "VALIDATION_FAILED", "reason is required; expiresAt must be in the future", "fix_input"))
		return
	}
	if src.State != "active" {
		h.fail(c, appErr(409, "RIGHTS_RESTRICTED", "a withdrawn source cannot be re-granted; register a new rights record", "new_source"))
		return
	}
	upd := map[string]any{"allow_student": in.AllowStudent, "allow_derivative": in.AllowDerivative, "allow_export": in.AllowExport, "allow_cross_org": in.AllowCrossOrg,
		"expires_at": in.ExpiresAt, "updated_at": h.Svc.now()}
	if in.Attribution != "" {
		upd["attribution"] = in.Attribution
	}
	if in.LicenseRef != "" {
		upd["license_ref"] = in.LicenseRef
	}
	if err := tx.Model(&src).Updates(upd).Error; err != nil {
		h.fail(c, err)
		return
	}
	tx.First(&src, "id = ?", src.ID)
	h.Svc.audit(tx, a, "source.rights", src.ID, in.Reason)
	reply(c, 200, gin.H{"source": src, "dependents": sourceDependents(tx, a.TenantID, src.ID)}, "ok")
}

func (h *API) withdrawSource(c *gin.Context) {
	a, tx, ok := h.teacherOrAdmin(c)
	if !ok {
		return
	}
	src, ok := h.loadSource(c, tx, a)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&in)
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Reason == "" || len([]rune(in.Reason)) > 200 {
		h.fail(c, appErr(400, "VALIDATION_FAILED", "reason 1..200 chars is required", "fix_input"))
		return
	}
	res := tx.Model(&model.EduSourceAsset{}).Where("id = ? AND state = 'active'", src.ID).
		Updates(map[string]any{"state": "withdrawn", "withdrawn_reason": in.Reason, "updated_at": h.Svc.now()})
	if res.Error != nil {
		h.fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		h.fail(c, appErr(409, "REVISION_CONFLICT", "source already withdrawn", "reload"))
		return
	}
	h.Svc.audit(tx, a, "source.withdraw", src.ID, in.Reason)
	src.State, src.WithdrawnReason = "withdrawn", in.Reason
	reply(c, 200, gin.H{"source": src, "dependents": sourceDependents(tx, a.TenantID, src.ID)}, "ok")
}

func (h *API) linkProblemSource(c *gin.Context) {
	a, tx, ok := h.teacherOrAdmin(c)
	if !ok {
		return
	}
	var in struct {
		SourceAssetID string `json:"sourceAssetId"`
	}
	if c.ShouldBindJSON(&in) != nil || in.SourceAssetID == "" {
		h.fail(c, appErr(400, "VALIDATION_FAILED", "sourceAssetId required", "fix_input"))
		return
	}
	var p model.EduProblem
	if tx.First(&p, "id = ? AND tenant_id = ? AND workspace_id = ?", c.Param("id"), a.TenantID, a.WorkspaceID).Error != nil {
		h.fail(c, ErrNotFound)
		return
	}
	var src model.EduSourceAsset
	if tx.First(&src, "id = ? AND tenant_id = ? AND workspace_id = ? AND state = 'active'", in.SourceAssetID, a.TenantID, a.WorkspaceID).Error != nil {
		h.fail(c, ErrNotFound)
		return
	}
	if p.SourceAssetID != "" && p.SourceAssetID != src.ID {
		// Re-pointing would silently lift restrictions from published content.
		h.fail(c, appErr(409, "RIGHTS_RESTRICTED", "problem already has a rights record; create a new problem for other material", "new_problem"))
		return
	}
	if err := tx.Model(&p).Update("source_asset_id", src.ID).Error; err != nil {
		h.fail(c, err)
		return
	}
	h.Svc.audit(tx, a, "source.link", p.ID, src.ID)
	reply(c, 200, gin.H{"problemId": p.ID, "sourceAssetId": src.ID}, "ok")
}

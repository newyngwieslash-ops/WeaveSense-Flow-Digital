package edumath

import (
	"time"

	"github.com/gin-gonic/gin"

	"yingce/backend/internal/model"
)

// Read only lists the web class management view needs. Invite secrets are never listed:
// the join code is shown once at creation (only its hash is stored).

func (h *API) registerAdminLists(g *gin.RouterGroup) {
	g.GET("/classes/:id/invites", h.listInvites)
	g.GET("/publications", h.listPublications)
}

func (h *API) listInvites(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	k, err := classForStaff(tx, a, c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	var rows []model.EduClassInvite
	tx.Where("tenant_id = ? AND class_id = ?", a.TenantID, k.ID).Order("created_at desc").Limit(50).Find(&rows)
	now := h.Svc.now()
	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		state := r.Status
		if state == "active" && (!now.Before(r.ExpiresAt) || r.Uses >= r.MaxUses) {
			state = "exhausted_or_expired"
		}
		out = append(out, gin.H{"id": r.ID, "status": state, "expiresAt": r.ExpiresAt, "maxUses": r.MaxUses, "uses": r.Uses, "createdAt": r.CreatedAt})
	}
	reply(c, 200, out, "ok")
}

// listPublications returns the published lessons of the current workspace (assignable sources).
func (h *API) listPublications(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	type row struct {
		ID               string    `json:"id"`
		LessonRevisionID string    `json:"lessonRevisionId"`
		LessonHash       string    `json:"lessonHash"`
		Title            string    `json:"title"`
		FamilyID         string    `json:"familyId"`
		CreatedAt        time.Time `json:"createdAt"`
	}
	var rows []row
	tx.Table("edu_publications p").
		Select("p.id, p.lesson_revision_id, p.lesson_hash, pr.title, pr.family_id, p.created_at").
		Joins("JOIN edu_lesson_revisions l ON l.id = p.lesson_revision_id AND l.tenant_id = p.tenant_id").
		Joins("JOIN edu_math_jobs j ON j.id = l.job_id AND j.tenant_id = p.tenant_id").
		Joins("JOIN edu_problem_revisions r ON r.id = l.problem_revision_id AND r.tenant_id = p.tenant_id").
		Joins("JOIN edu_problems pr ON pr.id = r.problem_id AND pr.tenant_id = p.tenant_id").
		Where("p.tenant_id = ? AND j.workspace_id = ? AND p.state = ?", a.TenantID, a.WorkspaceID, "published").
		Order("p.created_at desc").Limit(100).Scan(&rows)
	if rows == nil {
		rows = []row{}
	}
	reply(c, 200, rows, "ok")
}

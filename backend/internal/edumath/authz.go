package edumath

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

const reqDBKey = "edumath.reqdb"

var (
	ErrNoRole    = errors.New("edumath: education role required")
	ErrNotFound  = errors.New("edumath: not found")
	ErrForbidden = errors.New("edumath: forbidden")
	ErrConsent   = errors.New("edumath: guardian consent missing or withdrawn")
)

// Actor is the authenticated caller resolved for one request.
type Actor struct {
	UserID      string
	TenantID    string
	WorkspaceID string
	WsRole      string
	Roles       map[string]bool
}

func (a Actor) Has(roles ...string) bool {
	for _, r := range roles {
		if a.Roles[r] {
			return true
		}
	}
	return false
}

// eduTenantIDs = platform tenants of the user plus tenants where the user holds
// an active education role binding (students are not tenant members). Read in
// system context, filtered by user id — same pattern as flow.UserTenantIDs.
func eduTenantIDs(db *gorm.DB, userID string) ([]string, error) {
	ids, err := flow.UserTenantIDs(db, userID)
	if err != nil {
		return nil, err
	}
	var edu []string
	if err := db.Model(&model.EduRoleBinding{}).Where("user_id = ? AND status = ? AND (expires_at IS NULL OR expires_at > ?)", userID, "active", time.Now().UTC()).
		Distinct().Pluck("tenant_id", &edu).Error; err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []string{}
	for _, t := range append(ids, edu...) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, nil
}

// Middleware runs every authenticated /api/edu request in one transaction with
// app.tenant_ids = eduTenantIDs (Postgres FORCE RLS second layer). Handlers
// still authorize explicitly (role, class, enrollment, assignment, state).
func Middleware(db *gorm.DB, user flow.UserFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/edu/files/") {
			c.Next() // signed-URL fetch: authorized by signature + access recheck in system context
			return
		}
		u, err := user(c)
		if err != nil || u == nil {
			reply(c, http.StatusUnauthorized, nil, "login required")
			c.Abort()
			return
		}
		tenants, err := eduTenantIDs(db, u.ID)
		if err != nil {
			reply(c, http.StatusInternalServerError, nil, "tenant resolution failed")
			c.Abort()
			return
		}
		tx := db.Begin()
		if tx.Error != nil {
			reply(c, http.StatusServiceUnavailable, nil, "database unavailable")
			c.Abort()
			return
		}
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT set_config('app.tenant_ids', ?, true)", strings.Join(tenants, ",")).Error; err != nil {
				tx.Rollback()
				reply(c, http.StatusInternalServerError, nil, "tenant context failed")
				c.Abort()
				return
			}
		}
		c.Set(reqDBKey, tx)
		c.Set("edumath.user", u)
		defer func() {
			if r := recover(); r != nil {
				tx.Rollback()
				panic(r)
			}
		}()
		c.Next()
		if c.Writer.Status() >= 500 {
			tx.Rollback()
			return
		}
		tx.Commit()
	}
}

func reqDB(c *gin.Context, base *gorm.DB) *gorm.DB {
	if v, ok := c.Get(reqDBKey); ok {
		if tx, ok := v.(*gorm.DB); ok {
			return tx
		}
	}
	return base
}

func reply(c *gin.Context, status int, data any, msg string) {
	code := 0
	if status >= 400 {
		code = status
	}
	c.JSON(status, gin.H{"code": code, "data": data, "msg": msg})
}

func roles(tx *gorm.DB, tenantID, userID string) (map[string]bool, error) {
	return rolesIn(tx, tenantID, userID, "")
}

// rolesIn returns the user's effective education roles: active, not expired
// (DEV-013), and either tenant wide or scoped to workspaceID. With an empty
// workspaceID only tenant wide bindings count, so a workspace scoped role never
// leaks into another workspace.
func rolesIn(tx *gorm.DB, tenantID, userID, workspaceID string) (map[string]bool, error) {
	var rs []string
	q := tx.Model(&model.EduRoleBinding{}).Where("tenant_id = ? AND user_id = ? AND status = ? AND (expires_at IS NULL OR expires_at > ?)", tenantID, userID, "active", time.Now().UTC())
	if workspaceID == "" {
		q = q.Where("(workspace_id IS NULL OR workspace_id = '')")
	} else {
		q = q.Where("(workspace_id IS NULL OR workspace_id = '' OR workspace_id = ?)", workspaceID)
	}
	if err := q.Pluck("role", &rs).Error; err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, r := range rs {
		out[r] = true
	}
	return out, nil
}

// StaffActor resolves a teacher/reviewer/admin: workspace member (X-Workspace-Id)
// with an education role in that workspace's tenant.
func StaffActor(tx *gorm.DB, userID, workspaceID string) (Actor, error) {
	sc, err := flow.ResolveScope(tx, userID, workspaceID)
	if err != nil {
		return Actor{}, ErrForbidden
	}
	rs, err := rolesIn(tx, sc.TenantID, userID, sc.WorkspaceID)
	if err != nil {
		return Actor{}, err
	}
	a := Actor{UserID: userID, TenantID: sc.TenantID, WorkspaceID: sc.WorkspaceID, WsRole: sc.Role, Roles: rs}
	if !a.Has("teacher", "reviewer", "org_admin") {
		return a, ErrNoRole
	}
	return a, nil
}

// StudentActor resolves a student in an explicit institution context (X-Edu-Tenant).
// No workspace membership is consulted or granted.
func StudentActor(tx *gorm.DB, userID, tenantID string) (Actor, error) {
	if tenantID == "" {
		return Actor{}, ErrForbidden
	}
	rs, err := roles(tx, tenantID, userID)
	if err != nil {
		return Actor{}, err
	}
	if !rs["student"] {
		return Actor{}, ErrNoRole
	}
	return Actor{UserID: userID, TenantID: tenantID, Roles: rs}, nil
}

// canManageRoles: tenant owner/admin (platform membership) or education org_admin.
func canManageRoles(tx *gorm.DB, tenantID, userID string) bool {
	var n int64
	tx.Model(&model.Membership{}).Where("tenant_id = ? AND user_id = ? AND status = ? AND role IN ?", tenantID, userID, "active", []string{"owner", "admin"}).Count(&n)
	if n > 0 {
		return true
	}
	if tenantID == model.PersonalTenantID(userID) {
		return true
	}
	rs, _ := roles(tx, tenantID, userID)
	return rs["org_admin"]
}

// studentCanAccessAssignment is the server-side audience check for constraint 1:
// active student role, active enrollment in an active class, active assignment,
// published publication, and an active teaching assignment for the class.
func studentCanAccessAssignment(tx *gorm.DB, tenantID, userID, assignmentID string) (model.EduAssignment, model.EduPublication, error) {
	var as model.EduAssignment
	var pub model.EduPublication
	if err := tx.First(&as, "id = ? AND tenant_id = ? AND state = ?", assignmentID, tenantID, "active").Error; err != nil {
		return as, pub, ErrNotFound
	}
	rs, err := roles(tx, tenantID, userID)
	if err != nil || !rs["student"] {
		return as, pub, ErrNotFound
	}
	if !consentOK(tx, tenantID, userID) {
		return as, pub, ErrConsent
	}
	var n int64
	tx.Table("edu_enrollments e").Joins("JOIN edu_classes k ON k.id = e.class_id AND k.status = 'active' AND k.tenant_id = e.tenant_id").
		Where("e.tenant_id = ? AND e.class_id = ? AND e.student_user_id = ? AND e.status = 'active'", tenantID, as.ClassID, userID).Count(&n)
	if n == 0 {
		return as, pub, ErrNotFound
	}
	var t int64
	tx.Model(&model.EduTeachingAssignment{}).Where("tenant_id = ? AND class_id = ? AND status = 'active'", tenantID, as.ClassID).Count(&t)
	if t == 0 {
		return as, pub, ErrNotFound
	}
	if err := tx.First(&pub, "id = ? AND tenant_id = ? AND state = ?", as.PublicationID, tenantID, "published").Error; err != nil {
		return as, pub, ErrNotFound
	}
	// Source rights are checked at every student read (DEV-026): expiry or withdrawal stops access at once.
	var l model.EduLessonRevision
	if tx.Select("id", "problem_revision_id").First(&l, "id = ? AND tenant_id = ?", pub.LessonRevisionID, tenantID).Error != nil ||
		rightsAllow(tx, tenantID, l.ProblemRevisionID, "student") != nil {
		return as, pub, ErrNotFound
	}
	return as, pub, nil
}

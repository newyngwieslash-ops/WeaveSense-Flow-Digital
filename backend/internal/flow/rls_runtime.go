package flow

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"yingce/backend/internal/model"
)

const reqDBKey = "flow.reqdb"

// TenantMiddleware runs every authenticated flow request inside one
// transaction whose app.tenant_ids is the caller's active tenants, so the
// Postgres RLS policies (schema v58/v59) are enforced on everything the
// handler reads or writes — even a query that forgets its tenant filter.
// Only the explicitly registered service callbacks defer admission to their
// signed-context handler. They never inherit an unscoped business handle.
// The transaction commits for status < 500 and rolls back otherwise.
func TenantMiddleware(db *gorm.DB, user UserFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deferredServiceAdmission(c) {
			c.Next()
			return
		}
		u, err := user(c)
		if err != nil || u == nil {
			reply(c, http.StatusUnauthorized, nil, "unauthorized")
			c.Abort()
			return
		}
		if independentAdmission(c) {
			// Create/accept performs its own narrow admission and transaction,
			// never switches the scope of an existing user transaction.
			c.Next()
			return
		}
		tenants, err := UserTenantIDs(db, u.ID)
		if err != nil {
			if errors.Is(err, ErrForbidden) {
				reply(c, http.StatusForbidden, nil, "no active tenant membership")
			} else {
				reply(c, http.StatusInternalServerError, nil, "tenant resolution failed")
			}
			c.Abort()
			return
		}
		if len(tenants) == 0 {
			// Empty scope is never an authenticated business context, even
			// when the database independently rejects all tenant rows.
			reply(c, http.StatusForbidden, nil, "no active tenant membership")
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

func deferredServiceAdmission(c *gin.Context) bool {
	if c.Request.Method != http.MethodPost {
		return false
	}
	switch c.FullPath() {
	case "/api/internal/flow/events", "/api/internal/flow/authorize",
		"/api/internal/flow/budget/reserve", "/api/internal/flow/budget/close",
		"/api/internal/flow/summarize":
		return true
	}
	return false
}

func independentAdmission(c *gin.Context) bool {
	return c.Request.Method == http.MethodPost &&
		(c.FullPath() == "/api/intelligence/orgs" || c.FullPath() == "/api/intelligence/invites/accept")
}

// Missing middleware is a configuration error, never an unscoped fallback.
func reqDB(c *gin.Context, base *gorm.DB) *gorm.DB {
	if v, ok := c.Get(reqDBKey); ok {
		if tx, ok := v.(*gorm.DB); ok && tx != nil {
			return tx
		}
	}
	denied := base.Session(&gorm.Session{NewDB: true})
	denied.AddError(ErrForbidden)
	return denied
}

func (t Tools) withDB(db *gorm.DB) Tools {
	t.DB = db
	t.MCP = t.MCP.withDB(db)
	return t
}

func (g *MCPGateway) withDB(db *gorm.DB) *MCPGateway {
	if g == nil {
		return nil
	}
	c := *g
	c.DB = db
	return &c
}

// UserTenantIDs are the tenants a user may touch: active memberships plus the
// personal tenant (which exists implicitly before first use).
func UserTenantIDs(db *gorm.DB, userID string) ([]string, error) {
	if userID == "" {
		return nil, ErrForbidden
	}
	var tenants []string
	if db.Dialector.Name() == "postgres" {
		if err := db.Raw("SELECT tenant_id FROM flow_system.resolve_user_tenants(?)", userID).Scan(&tenants).Error; err != nil {
			return nil, err
		}
		return tenants, nil
	}
	var actor model.User
	if err := db.First(&actor, "id = ? AND status = ?", userID, "active").Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrForbidden
		}
		return nil, err
	}
	if err := db.Table("flow_memberships m").
		Joins("JOIN flow_tenants t ON t.id = m.tenant_id AND t.status = 'active'").
		Where("m.user_id = ? AND m.status = ?", userID, "active").Pluck("m.tenant_id", &tenants).Error; err != nil {
		return nil, err
	}
	// Only an absent personal tenant may be implicitly created. An existing
	// disabled tenant or revoked membership must never be restored by fallback.
	var personal model.Tenant
	err := db.First(&personal, "id = ?", model.PersonalTenantID(userID)).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return append(tenants, model.PersonalTenantID(userID)), nil
	}
	if err != nil {
		return nil, err
	}
	return tenants, nil
}

// UserTx runs fn in a transaction scoped (RLS app.tenant_ids) to the user's
// tenants — the background equivalent of TenantMiddleware, used by agent tool
// execution, conversation recording and auto-continue outside HTTP requests.
func UserTx(db *gorm.DB, userID string, fn func(tx *gorm.DB) error) error {
	if userID == "" {
		return ErrForbidden
	}
	tenants, err := UserTenantIDs(db, userID)
	if err != nil {
		return err
	}
	if len(tenants) == 0 {
		return ErrForbidden
	}
	return TenantTx(db, strings.Join(tenants, ","), fn)
}

// WithDB rebinds the tool registry (and its MCP gateway) to a scoped handle.
func (t Tools) WithDB(db *gorm.DB) Tools { return t.withDB(db) }

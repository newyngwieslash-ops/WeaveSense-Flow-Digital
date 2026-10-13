package flow

import (
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
// Unauthenticated requests pass through untouched (handlers reject them);
// engine callbacks (/internal/...) authenticate by token and stay in system
// context. The transaction commits for status < 500 and rolls back otherwise.
func TenantMiddleware(db *gorm.DB, user UserFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.Contains(c.Request.URL.Path, "/internal/") {
			c.Next()
			return
		}
		u, err := user(c)
		if err != nil || u == nil {
			c.Next()
			return
		}
		var tenants []string
		if err := db.Model(&model.Membership{}).Where("user_id = ? AND status = ?", u.ID, "active").Pluck("tenant_id", &tenants).Error; err != nil {
			reply(c, http.StatusInternalServerError, nil, "tenant resolution failed")
			c.Abort()
			return
		}
		// A user's personal tenant exists implicitly before first use.
		tenants = append(tenants, model.PersonalTenantID(u.ID))
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

// reqDB returns the request's tenant-scoped transaction, or base when the
// request is not tenant-scoped (unauthenticated, internal, background).
func reqDB(c *gin.Context, base *gorm.DB) *gorm.DB {
	if v, ok := c.Get(reqDBKey); ok {
		if tx, ok := v.(*gorm.DB); ok {
			return tx
		}
	}
	return base
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

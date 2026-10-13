package flow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"yingce/backend/internal/model"
)

func internalServiceCtx(c *gin.Context, cfg Config) (ExecutionContext, bool) {
	tok := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	ctx, err := Verify(cfg.SigningKey, tok, cfg.Issuer, time.Now())
	if err != nil || ctx.Actor.Type != "service_principal" {
		reply(c, http.StatusUnauthorized, nil, "invalid context")
		return ExecutionContext{}, false
	}
	return ctx, true
}

// RegisterP5Routes mounts billing, org/invite, notification and the
// engine-facing authorize/summarize endpoints.
func RegisterP5Routes(api gin.IRouter, db *gorm.DB, cfg Config, user UserFunc, gw ModelGateway) {
	billing := Billing{DB: db}
	authed := func(c *gin.Context) (*model.User, bool) {
		u, err := user(c)
		if err != nil || u == nil {
			reply(c, http.StatusUnauthorized, nil, "unauthorized")
			return nil, false
		}
		return u, true
	}
	scoped := func(c *gin.Context) (Scope, *model.User, bool) {
		u, ok := authed(c)
		if !ok {
			return Scope{}, nil, false
		}
		s, err := ResolveScope(db, u.ID, c.GetHeader("X-Workspace-Id"))
		if err != nil {
			reply(c, http.StatusNotFound, nil, "workspace not found")
			return Scope{}, nil, false
		}
		return s, u, true
	}
	g := api.Group("/intelligence")
	g.POST("/notifications/:id/read", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		billing := Billing{DB: db, Now: billing.Now}
		_ = billing
		s, u, ok := scoped(c)
		if !ok {
			return
		}
		var n int64
		err := TenantTx(db, s.TenantID, func(tx *gorm.DB) (e error) { n, e = MarkNotificationsRead(tx, s, u.ID, c.Param("id")); return })
		if err != nil {
			reply(c, http.StatusInternalServerError, nil, "update error")
			return
		}
		reply(c, http.StatusOK, gin.H{"updated": n}, "ok")
	})
	g.GET("/billing", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		billing := Billing{DB: db, Now: billing.Now}
		_ = billing
		s, _, ok := scoped(c)
		if !ok {
			return
		}
		acc, err := billing.Account(db, s.TenantID)
		if err != nil {
			reply(c, http.StatusInternalServerError, nil, "billing error")
			return
		}
		var rows []model.FlowBudgetReservation
		db.Where("tenant_id = ?", s.TenantID).Order("created_at DESC").Limit(50).Find(&rows)
		reply(c, http.StatusOK, gin.H{"account": acc, "reservations": rows}, "ok")
	})
	g.POST("/orgs", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		billing := Billing{DB: db, Now: billing.Now}
		_ = billing
		u, ok := authed(c)
		if !ok {
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		_ = c.ShouldBindJSON(&body)
		if strings.TrimSpace(body.Name) == "" {
			reply(c, http.StatusBadRequest, nil, "name required")
			return
		}
		t, ws, err := CreateOrganization(db, u.ID, body.Name)
		if err != nil {
			reply(c, http.StatusInternalServerError, nil, "create failed")
			return
		}
		reply(c, http.StatusOK, gin.H{"tenant": t, "workspace": ws}, "ok")
	})
	g.POST("/invites", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		billing := Billing{DB: db, Now: billing.Now}
		_ = billing
		s, u, ok := scoped(c)
		if !ok {
			return
		}
		var body struct{ Email, Role string }
		_ = c.ShouldBindJSON(&body)
		inv, tok, err := CreateInvite(db, s, u.ID, body.Email, body.Role, 7*24*time.Hour)
		if err != nil {
			reply(c, http.StatusForbidden, nil, err.Error())
			return
		}
		// The token is shown once to the inviting admin; only its hash is stored.
		reply(c, http.StatusOK, gin.H{"invite": inv, "token": tok}, "ok")
	})
	g.POST("/invites/accept", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		billing := Billing{DB: db, Now: billing.Now}
		_ = billing
		u, ok := authed(c)
		if !ok {
			return
		}
		var body struct{ Token string }
		_ = c.ShouldBindJSON(&body)
		s, err := AcceptInvite(db, u.ID, body.Token)
		if err != nil {
			reply(c, http.StatusBadRequest, nil, "invite invalid or used")
			return
		}
		reply(c, http.StatusOK, s, "ok")
	})
	g.DELETE("/members/:userId", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		billing := Billing{DB: db, Now: billing.Now}
		_ = billing
		s, _, ok := scoped(c)
		if !ok {
			return
		}
		if err := RemoveMember(db, s, c.Param("userId")); err != nil {
			reply(c, http.StatusForbidden, nil, err.Error())
			return
		}
		reply(c, http.StatusOK, nil, "ok")
	})

	// Engine → platform: does the schedule's grant actor still hold the scope?
	api.POST("/internal/flow/authorize", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		billing := Billing{DB: db, Now: billing.Now}
		_ = billing
		ctx, ok := internalServiceCtx(c, cfg)
		if !ok {
			return
		}
		var body struct {
			GrantActor  string `json:"grant_actor"`
			WorkspaceID string `json:"workspace_id"`
			Scope       string `json:"scope"`
		}
		if err := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<16)).Decode(&body); err != nil {
			reply(c, http.StatusBadRequest, nil, "bad request")
			return
		}
		if err := AuthorizeGrant(db, body.GrantActor, ctx.TenantID, body.WorkspaceID, body.Scope); err != nil {
			reply(c, http.StatusForbidden, gin.H{"allowed": false}, "grant revoked")
			return
		}
		reply(c, http.StatusOK, gin.H{"allowed": true}, "ok")
	})
	// Engine → platform: LLM brief summarization through the Model Gateway,
	// billed to the tenant and citation-validated before returning.
	api.POST("/internal/flow/summarize", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		billing := Billing{DB: db, Now: billing.Now}
		_ = billing
		ctx, ok := internalServiceCtx(c, cfg)
		if !ok {
			return
		}
		var body struct {
			GrantActor  string             `json:"grant_actor"`
			WorkspaceID string             `json:"workspace_id"`
			Key         string             `json:"key"`
			Topic       string             `json:"topic"`
			Scope       MaterialScope      `json:"scope"`
			Evidence    []ResolvedEvidence `json:"evidence"`
		}
		if err := json.NewDecoder(io.LimitReader(c.Request.Body, 4<<20)).Decode(&body); err != nil || body.Key == "" {
			reply(c, http.StatusBadRequest, nil, "bad request")
			return
		}
		if err := AuthorizeGrant(db, body.GrantActor, ctx.TenantID, body.WorkspaceID, "intel.reports.write"); err != nil {
			reply(c, http.StatusForbidden, nil, "grant revoked")
			return
		}
		if gw == nil {
			reply(c, http.StatusServiceUnavailable, nil, "model gateway not configured")
			return
		}
		cctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
		defer cancel()
		out, err := Synthesize(cctx, gw, billing, SynthesisRequest{TenantID: ctx.TenantID, Key: "brief:" + body.Key, Purpose: "brief",
			Question: body.Topic, Evidence: body.Evidence, Scope: body.Scope})
		switch {
		case errors.Is(err, ErrBudgetInsufficient), errors.Is(err, ErrBudgetQuota):
			reply(c, http.StatusPaymentRequired, nil, err.Error())
		case err != nil:
			reply(c, http.StatusBadGateway, nil, err.Error())
		default:
			reply(c, http.StatusOK, out, "ok")
		}
	})
}

// RunBillingMaintenance closes reservations of runs that died without
// completing, and conservatively charges unknown outcomes older than 24h
// (the gateway offers no status lookup, so the reserved amount is billed).
func RunBillingMaintenance(ctx context.Context, db *gorm.DB, every time.Duration) {
	b := Billing{DB: db}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		_, _ = b.SweepStale(6 * time.Hour)
		// No status endpoint / still unknown after 24h: charge conservatively.
		_, _ = b.Reconcile(24*time.Hour, func(r model.FlowBudgetReservation) (Outcome, error) {
			return Outcome{Known: true, Succeeded: true, Cost: r.ReservedMicro}, nil
		})
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

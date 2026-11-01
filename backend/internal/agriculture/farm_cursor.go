package agriculture

import (
	"context"
	"gorm.io/gorm"
	"time"
)

const farmCursorType = "weavesense-agri-farm-cursor+jws"

type farmCursor struct {
	SchemaVersion        string `json:"schema_version"`
	ActorID              string `json:"actor_user_id"`
	TenantID             string `json:"tenant_id"`
	WorkspaceID          string `json:"workspace_id"`
	AuthorizationVersion int64  `json:"authorization_version"`
	Status               string `json:"status"`
	Search               string `json:"search"`
	AfterID              string `json:"after_id"`
	ExpiresAt            int64  `json:"expires_at"`
}

func (p *FarmPlatform) cursorScope(ctx context.Context, actor, ws, status, search, token string) (farmCursor, error) {
	var current farmCursor
	e := p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		version, e := currentAuthorizationVersion(tx, s.Tenant.ID)
		if e != nil {
			return e
		}
		current = farmCursor{"1.0", actor, s.Tenant.ID, ws, version, status, search, "", p.Service.now().Add(15 * time.Minute).Unix()}
		return nil
	})
	if e != nil {
		return current, e
	}
	if token != "" {
		if p.Client == nil || len(token) > 8192 {
			return current, ErrInvalid
		}
		var old farmCursor
		if _, e := p.Client.keys.verify(token, farmCursorType, p.Service.now(), &old); e != nil {
			return current, ErrInvalid
		}
		if old.SchemaVersion != "1.0" || old.ActorID != actor || old.TenantID != current.TenantID || old.WorkspaceID != ws || old.Status != status || old.Search != search || !ownershipID.MatchString(old.AfterID) || old.ExpiresAt <= p.Service.now().Unix() {
			return current, ErrInvalid
		}
		if old.AuthorizationVersion != current.AuthorizationVersion {
			return current, ErrConflict
		}
		current.AfterID = old.AfterID
	}
	return current, nil
}

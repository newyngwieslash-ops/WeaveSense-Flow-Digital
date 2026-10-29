package agriculture

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"

	"gorm.io/gorm"
	"yingce/backend/internal/model"
)

type BindingPage struct {
	Items      []model.AgricultureResourceBinding
	NextCursor string
	HasMore    bool
}

// This opaque position is not a credential or grant. Every page rechecks the
// trusted actor, current governance, tenant/workspace and authorization epoch;
// database queries always carry their own exact scope. No cursor field can
// choose the query's actor or tenant, even if a caller decodes/edits the cursor.
type bindingCursor struct {
	SchemaVersion        string `json:"schema_version"`
	ActorID              string `json:"actor_user_id"`
	TenantID             string `json:"tenant_id"`
	WorkspaceID          string `json:"workspace_id"`
	AuthorizationVersion int64  `json:"authorization_version"`
	Offset               int64  `json:"offset"`
	Sort                 string `json:"sort"`
	Filter               string `json:"filter"`
}

func decodeBindingCursor(value string) (bindingCursor, error) {
	var out bindingCursor
	if len(value) > 1400 {
		return out, ErrInvalid
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(raw) > 1024 {
		return out, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil {
		return out, ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || out.SchemaVersion != "1.0" || out.Sort != "created_at,id" || out.Filter != "all" || out.Offset < 0 || out.Offset > maximumAuthorizationVersion || out.ActorID == "" || out.TenantID == "" || out.WorkspaceID == "" || out.AuthorizationVersion < 1 || out.AuthorizationVersion > maximumAuthorizationVersion {
		return out, ErrInvalid
	}
	return out, nil
}

// limit=0 selects the standard default. The API adapter rejects explicitly
// supplied zero/negative limits rather than silently interpreting them as absent.
func (s Service) ListBindingPage(ctx context.Context, actorID, workspaceID string, limit int, cursor string) (BindingPage, error) {
	out := BindingPage{Items: []model.AgricultureResourceBinding{}}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return out, ErrInvalid
	}
	var position bindingCursor
	if cursor != "" {
		var err error
		position, err = decodeBindingCursor(cursor)
		if err != nil {
			return out, err
		}
	}
	err := s.withWorkspace(ctx, actorID, workspaceID, true, func(tx *gorm.DB, scope workspaceContext) error {
		if !scope.Owner && !hasRole(scope.Roles, "tenant_admin") {
			return ErrForbidden
		}
		version, err := currentAuthorizationVersion(tx, scope.Tenant.ID)
		if err != nil {
			return err
		}
		if cursor != "" {
			if position.ActorID != actorID || position.TenantID != scope.Tenant.ID || position.WorkspaceID != workspaceID {
				return ErrInvalid
			}
			if position.AuthorizationVersion != version {
				return ErrConflict
			}
		}
		query := tx.Model(&model.AgricultureResourceBinding{}).Where("tenant_id=? AND workspace_id=?", scope.Tenant.ID, workspaceID)
		var count int64
		if err := query.Count(&count).Error; err != nil {
			return err
		}
		if position.Offset > count {
			return ErrInvalid
		}
		// Count bounds conversion too on supported 64-bit deployments; reject
		// values that do not round-trip on any narrower platform.
		offset := int(position.Offset)
		if int64(offset) != position.Offset {
			return ErrInvalid
		}
		if err := tx.Where("tenant_id=? AND workspace_id=?", scope.Tenant.ID, workspaceID).Order("created_at,id").Limit(limit).Offset(offset).Find(&out.Items).Error; err != nil {
			return err
		}
		next := position.Offset + int64(len(out.Items))
		out.HasMore = next < count
		if out.HasMore {
			raw, err := json.Marshal(bindingCursor{SchemaVersion: "1.0", ActorID: actorID, TenantID: scope.Tenant.ID, WorkspaceID: workspaceID, AuthorizationVersion: version, Offset: next, Sort: "created_at,id", Filter: "all"})
			if err != nil {
				return err
			}
			out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
		}
		return nil
	})
	if err != nil {
		return BindingPage{}, err
	}
	return out, nil
}

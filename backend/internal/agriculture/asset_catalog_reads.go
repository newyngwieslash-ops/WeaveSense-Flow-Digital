package agriculture

import (
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"sort"
	"time"
	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

func (p *AssetCatalogPlatform) actions(ctx context.Context, actor, ws, id string, historical bool) ([]string, error) {
	if _, e := p.Authorize(ctx, actor, ws, "agri.asset.read", []string{id}); e != nil {
		return nil, e
	}
	out := []string{"agri.asset.read"}
	caps := []string{"agri.asset_download.read"}
	if !historical {
		caps = append(caps, "agri.asset.create")
	}
	for _, cap := range caps {
		if _, e := p.Authorize(ctx, actor, ws, cap, []string{id}); e == nil {
			out = append(out, cap)
		} else if !errors.Is(e, ErrForbidden) && !errors.Is(e, ErrNotFound) {
			return nil, e
		}
	}
	sort.Strings(out)
	return out, nil
}
func (p *AssetCatalogPlatform) Read(ctx context.Context, actor, ws, id string, version int64) (map[string]any, error) {
	if !assetCatalogID.MatchString(id) || version < 1 || version > maxOwnershipRevision {
		return nil, ErrInvalid
	}
	decision, e := p.Authorize(ctx, actor, ws, "agri.asset.read", []string{id})
	if e != nil {
		return nil, e
	}
	if version > decision.ResourceVersions[0].Revision {
		return nil, ErrNotFound
	}
	body, _, _ := farmCanonical(map[string]any{"asset_id": id, "version": version})
	raw, e := p.workspaceCall(ctx, actor, ws, "asset.read", flow.NewID("req_"), zeroFarmHash, zeroFarmHash, []string{id}, body)
	if e != nil {
		return nil, e
	}
	value, e := parcelDecodeMap(raw)
	if e != nil {
		return nil, e
	}
	if value["asset_id"] != id || value["workspace_id"] != ws || value["version"] != float64(version) || value["revision"] != float64(decision.ResourceVersions[0].Revision) {
		return nil, ErrDependencyUnavailable
	}
	caps, e := p.actions(ctx, actor, ws, id, version != decision.ResourceVersions[0].Revision)
	if e != nil {
		return nil, e
	}
	value["allowed_actions"] = caps
	return value, nil
}
func (p *AssetCatalogPlatform) visibleIDs(ctx context.Context, actor, ws string) ([]string, error) {
	ids := []string{}
	e := p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		if !roleAllowsAction(s.Roles, "agri.asset.read") {
			return nil
		}
		var bindings []model.AgricultureResourceBinding
		if e := tx.Where("tenant_id=? AND workspace_id=? AND subject_type='user' AND subject_id=? AND resource_kind='asset' AND state='active' AND (expires_at IS NULL OR expires_at>?)", s.Tenant.ID, ws, actor, p.Service.now()).Order("resource_id").Find(&bindings).Error; e != nil {
			return e
		}
		for _, b := range bindings {
			caps, e := bindingCapabilities(b)
			if e != nil {
				return e
			}
			for _, cap := range caps {
				if cap == "agri.asset.read" && assetCatalogID.MatchString(b.ResourceID) {
					ids = append(ids, b.ResourceID)
				}
			}
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	valid := []string{}
	for _, id := range ids {
		if _, e := p.Authorize(ctx, actor, ws, "agri.asset.read", []string{id}); e == nil {
			valid = append(valid, id)
		} else if !errors.Is(e, ErrForbidden) && !errors.Is(e, ErrNotFound) && !errors.Is(e, ErrConflict) {
			return nil, e
		}
	}
	return valid, nil
}

type assetCatalogCursor struct {
	SchemaVersion string `json:"schema_version"`
	ActorID       string `json:"actor_id"`
	TenantID      string `json:"tenant_id"`
	WorkspaceID   string `json:"workspace_id"`
	Epoch         int64  `json:"epoch"`
	Kind          string `json:"kind"`
	DataClass     string `json:"data_class"`
	AfterID       string `json:"after_id"`
	ExpiresAt     int64  `json:"expires_at"`
}

func (p *AssetCatalogPlatform) List(ctx context.Context, actor, ws, kind, dataClass string, limit int, token string) (farmListPage, error) {
	empty := farmListPage{Items: []any{}, NextCursor: "", HasMore: false}
	if limit < 1 || limit > 200 || !(kind == "" || kind == "raster" || kind == "vector" || kind == "table" || kind == "photo" || kind == "audio" || kind == "model") || !(dataClass == "" || dataClass == "demo" || dataClass == "operational") {
		return empty, ErrInvalid
	}
	cursor := assetCatalogCursor{SchemaVersion: "1.0", ActorID: actor, WorkspaceID: ws, Kind: kind, DataClass: dataClass, ExpiresAt: p.Service.now().Add(15 * time.Minute).Unix()}
	e := p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		cursor.TenantID = s.Tenant.ID
		var e error
		cursor.Epoch, e = currentAuthorizationVersion(tx, s.Tenant.ID)
		return e
	})
	if e != nil {
		return empty, e
	}
	if token != "" {
		if p.Client == nil || len(token) > 8192 {
			return empty, ErrInvalid
		}
		var prior assetCatalogCursor
		if _, e = p.Client.keys.verify(token, assetCatalogCursorType, p.Service.now(), &prior); e != nil {
			return empty, ErrInvalid
		}
		if prior.SchemaVersion != cursor.SchemaVersion || prior.ActorID != actor || prior.WorkspaceID != ws || prior.TenantID != cursor.TenantID || prior.Kind != kind || prior.DataClass != dataClass || !assetCatalogID.MatchString(prior.AfterID) || prior.ExpiresAt <= p.Service.now().Unix() {
			return empty, ErrInvalid
		}
		if prior.Epoch != cursor.Epoch {
			return empty, ErrConflict
		}
		cursor.AfterID = prior.AfterID
	}
	ids, e := p.visibleIDs(ctx, actor, ws)
	if e != nil {
		return empty, e
	}
	filtered := []string{}
	for _, id := range ids {
		if id > cursor.AfterID {
			filtered = append(filtered, id)
		}
	}
	items := []map[string]any{}
	more := false
	for offset := 0; offset < len(filtered) && !more; offset += 200 {
		end := offset + 200
		if end > len(filtered) {
			end = len(filtered)
		}
		chunk := filtered[offset:end]
		body, _, _ := farmCanonical(map[string]any{"asset_ids": chunk, "limit": 200, "after_id": cursor.AfterID})
		raw, e := p.workspaceCall(ctx, actor, ws, "asset.list", flow.NewID("req_"), zeroFarmHash, zeroFarmHash, chunk, body)
		if e != nil {
			return empty, e
		}
		var page struct {
			Items      []json.RawMessage `json:"items"`
			HasMore    bool              `json:"has_more"`
			NextCursor string            `json:"next_cursor"`
		}
		if strictFarmJSON(raw, &page, 1<<20) != nil || page.Items == nil || len(page.Items) > len(chunk) || page.HasMore {
			return empty, ErrDependencyUnavailable
		}
		allowed := map[string]bool{}
		for _, id := range chunk {
			allowed[id] = true
		}
		last := cursor.AfterID
		for _, encoded := range page.Items {
			item, e := parcelDecodeMap(encoded)
			if e != nil {
				return empty, e
			}
			id, ok := item["asset_id"].(string)
			if !ok || !allowed[id] || id <= last || item["workspace_id"] != ws {
				return empty, ErrDependencyUnavailable
			}
			last = id
			if (kind != "" && item["kind"] != kind) || (dataClass != "" && item["data_class"] != dataClass) {
				continue
			}
			if len(items) == limit {
				more = true
				break
			}
			caps, e := p.actions(ctx, actor, ws, id, false)
			if e != nil {
				return empty, e
			}
			item["allowed_actions"] = caps
			items = append(items, item)
		}
	}
	next := ""
	if more {
		cursor.AfterID = items[len(items)-1]["asset_id"].(string)
		next, e = p.Client.keys.sign(assetCatalogCursorType, cursor, p.Service.now(), time.Unix(cursor.ExpiresAt, 0))
		if e != nil {
			return empty, e
		}
	}
	return farmListPage{Items: items, NextCursor: next, HasMore: more}, nil
}
func (p *AssetCatalogPlatform) Context(ctx context.Context, actor, ws string) (map[string]any, error) {
	canRegister := false
	eligible := []map[string]any{}
	farmIDs := []string{}
	e := p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		canRegister = s.Owner || hasRole(s.Roles, "tenant_admin")
		var bindings []model.AgricultureResourceBinding
		if e := tx.Where("tenant_id=? AND workspace_id=? AND subject_type='user' AND subject_id=? AND resource_kind='farm' AND state='active' AND (expires_at IS NULL OR expires_at>?)", s.Tenant.ID, ws, actor, p.Service.now()).Order("resource_id").Find(&bindings).Error; e != nil {
			return e
		}
		for _, b := range bindings {
			caps, e := bindingCapabilities(b)
			if e != nil {
				return e
			}
			for _, cap := range caps {
				if cap == "agri.asset.create" && roleAllowsAction(s.Roles, cap) {
					farmIDs = append(farmIDs, b.ResourceID)
				}
			}
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	farms := []map[string]any{}
	canDelegate := canRegister
	for _, id := range farmIDs {
		d, e := p.Service.Authorize(ctx, actor, ws, "agri.asset.create", []ResourceRef{{"farm", id}})
		if e != nil {
			if errors.Is(e, ErrNotFound) || errors.Is(e, ErrForbidden) {
				continue
			}
			return nil, e
		}
		farms = append(farms, map[string]any{"farm_id": id, "name": id, "revision": d.ResourceVersions[0].Revision})
		if !canDelegate {
			_, e = p.Service.Authorize(ctx, actor, ws, "agri.authorization.update", []ResourceRef{{"farm", id}})
			canDelegate = e == nil
		}
	}
	if canDelegate {
		e = p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
			var members []model.WorkspaceMembership
			if e := tx.Where("workspace_id=?", ws).Find(&members).Error; e != nil {
				return e
			}
			for _, m := range members {
				target, e := resolve(tx, m.UserID, ws, false)
				if e != nil {
					continue
				}
				caps := []string{}
				for _, cap := range []string{"agri.asset.read", "agri.asset.create", "agri.asset_download.read"} {
					if roleAllowsAction(target.Roles, cap) {
						caps = append(caps, cap)
					}
				}
				if len(caps) == 0 {
					continue
				}
				var user model.User
				if tx.Where("id=?", m.UserID).First(&user).Error != nil {
					return ErrDependencyUnavailable
				}
				eligible = append(eligible, map[string]any{"user_id": m.UserID, "display_name": user.Username, "roles": target.Roles, "allowed_capabilities": caps})
			}
			return nil
		})
		if e != nil {
			return nil, e
		}
	}
	raw, e := p.workspaceCall(ctx, actor, ws, "asset.context", flow.NewID("req_"), zeroFarmHash, zeroFarmHash, []string{}, []byte("{}"))
	if e != nil {
		return nil, e
	}
	var context struct {
		Sources []struct {
			SourceID string `json:"source_id"`
			Kind     string `json:"kind"`
			Label    string `json:"label"`
		} `json:"sources"`
	}
	if strictFarmJSON(raw, &context, 1<<20) != nil || context.Sources == nil {
		return nil, ErrDependencyUnavailable
	}
	return map[string]any{"workspace_id": ws, "can_register_without_farm": canRegister, "eligible_grantees": eligible, "farms": farms, "sources": context.Sources}, nil
}

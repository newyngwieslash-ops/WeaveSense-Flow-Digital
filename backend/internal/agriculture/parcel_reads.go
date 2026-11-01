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

type parcelCursor struct {
	Version         string `json:"version"`
	ActorID         string `json:"actor_id"`
	TenantID        string `json:"tenant_id"`
	WorkspaceID     string `json:"workspace_id"`
	Kind            string `json:"kind"`
	ResourceID      string `json:"resource_id"`
	Epoch           int64  `json:"epoch"`
	CurrentRevision int64  `json:"current_revision"`
	AfterID         string `json:"after_id"`
	AfterRevision   int64  `json:"after_revision"`
	ExpiresAt       int64  `json:"expires_at"`
}

func (p *ParcelPlatform) cursor(ctx context.Context, actor, ws, kind, id, token string, current int64) (parcelCursor, error) {
	var value parcelCursor
	e := p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		epoch, e := currentAuthorizationVersion(tx, s.Tenant.ID)
		if e != nil {
			return e
		}
		value = parcelCursor{Version: "1.0", ActorID: actor, TenantID: s.Tenant.ID, WorkspaceID: ws, Kind: kind, ResourceID: id, Epoch: epoch, CurrentRevision: current, ExpiresAt: p.Service.now().Add(15 * time.Minute).Unix()}
		return nil
	})
	if e != nil {
		return value, e
	}
	if token != "" {
		if p.Client == nil || len(token) > 8192 {
			return value, ErrInvalid
		}
		var prior parcelCursor
		if _, e = p.Client.keys.verify(token, parcelCursorType, p.Service.now(), &prior); e != nil {
			return value, ErrInvalid
		}
		validAfter := prior.AfterID == "" || parcelIDPattern.MatchString(prior.AfterID)
		if kind == "parents" {
			validAfter = prior.AfterID == "" || ownershipID.MatchString(prior.AfterID)
		}
		if prior.Version != value.Version || prior.ActorID != actor || prior.TenantID != value.TenantID || prior.WorkspaceID != ws || prior.Kind != kind || prior.ResourceID != id || prior.ExpiresAt <= p.Service.now().Unix() || prior.AfterRevision < 0 || !validAfter {
			return value, ErrInvalid
		}
		if prior.Epoch != value.Epoch || prior.CurrentRevision != current {
			return value, ErrConflict
		}
		value.AfterID = prior.AfterID
		value.AfterRevision = prior.AfterRevision
	}
	return value, nil
}
func (p *ParcelPlatform) signedCursor(value parcelCursor) (string, error) {
	if p.Client == nil {
		return "", ErrDependencyUnavailable
	}
	return p.Client.keys.sign(parcelCursorType, value, p.Service.now(), time.Unix(value.ExpiresAt, 0))
}

func (p *ParcelPlatform) parcelActions(ctx context.Context, actor, ws, id string, historical bool) ([]string, error) {
	actions := []string{"agri.parcel.read"}
	if _, e := p.Service.Authorize(ctx, actor, ws, "agri.parcel.read", []ResourceRef{{"parcel", id}}); e != nil {
		return nil, e
	}
	if !historical {
		if _, e := p.Service.Authorize(ctx, actor, ws, "agri.parcel.update", []ResourceRef{{"parcel", id}}); e == nil {
			actions = append(actions, "agri.parcel.update")
		} else if !errors.Is(e, ErrForbidden) && !errors.Is(e, ErrNotFound) {
			return nil, e
		}
	}
	return actions, nil
}

func (p *ParcelPlatform) Read(ctx context.Context, actor, ws, id string, revision int64, at string) (map[string]any, error) {
	if !parcelIDPattern.MatchString(id) || revision < 0 || revision > maximumAuthorizationVersion || (revision > 0 && at != "") {
		return nil, ErrInvalid
	}
	if at != "" {
		parsed, e := time.Parse(time.RFC3339Nano, at)
		if e != nil || parsed.Location() != time.UTC || len(at) > 40 {
			return nil, ErrInvalid
		}
	}
	decision, e := p.Service.Authorize(ctx, actor, ws, "agri.parcel.read", []ResourceRef{{"parcel", id}})
	if e != nil {
		return nil, e
	}
	body, _, e := farmCanonical(map[string]any{"revision": revision, "at": at})
	if e != nil {
		return nil, e
	}
	raw, e := p.Client.farmAuthorized(ctx, p.Service, actor, decision, "POST", "/internal/agri/v1/parcels/"+id+"/read", body, flow.NewID("req_"))
	if e != nil {
		return nil, e
	}
	result, e := parcelDecodeMap(raw)
	if e != nil {
		return nil, e
	}
	current, ok := result["current_geometry_revision"].(float64)
	selected, selectedOK := result["geometry_revision"].(float64)
	if !ok || !selectedOK || selected < 1 || selected > current || result["id"] != id || result["workspace_id"] != ws || len(decision.FarmIDs) != 1 || result["farm_id"] != decision.FarmIDs[0] || len(decision.ResourceVersions) != 1 || current != float64(decision.ResourceVersions[0].Revision) {
		return nil, ErrDependencyUnavailable
	}
	actions, e := p.parcelActions(ctx, actor, ws, id, selected != current)
	if e != nil {
		return nil, e
	}
	result["allowed_actions"] = actions
	return result, nil
}

func (p *ParcelPlatform) History(ctx context.Context, actor, ws, id string, limit int, token string) (farmListPage, error) {
	empty := farmListPage{Items: []any{}, NextCursor: "", HasMore: false}
	if !parcelIDPattern.MatchString(id) || limit < 1 || limit > 200 {
		return empty, ErrInvalid
	}
	decision, e := p.Service.Authorize(ctx, actor, ws, "agri.parcel.read", []ResourceRef{{"parcel", id}})
	if e != nil {
		return empty, e
	}
	if len(decision.ResourceVersions) != 1 {
		return empty, ErrDependencyUnavailable
	}
	cursor, e := p.cursor(ctx, actor, ws, "history", id, token, decision.ResourceVersions[0].Revision)
	if e != nil {
		return empty, e
	}
	body, _, _ := farmCanonical(map[string]any{"after_revision": cursor.AfterRevision, "limit": limit})
	raw, e := p.Client.farmAuthorized(ctx, p.Service, actor, decision, "POST", "/internal/agri/v1/parcels/"+id+"/history", body, flow.NewID("req_"))
	if e != nil {
		return empty, e
	}
	var page struct {
		Items             []json.RawMessage `json:"items"`
		NextAfterRevision int64             `json:"next_after_revision"`
		HasMore           bool              `json:"has_more"`
	}
	if strictFarmJSON(raw, &page, 1<<20) != nil || page.Items == nil || len(page.Items) > limit {
		return empty, ErrDependencyUnavailable
	}
	items := []map[string]any{}
	last := cursor.AfterRevision
	for _, encoded := range page.Items {
		item, e := parcelDecodeMap(encoded)
		if e != nil {
			return empty, e
		}
		rev, ok := item["geometry_revision"].(float64)
		if !ok || rev != float64(int64(rev)) || rev <= float64(last) || rev > float64(cursor.CurrentRevision) || item["id"] != id || item["workspace_id"] != ws || len(decision.FarmIDs) != 1 || item["farm_id"] != decision.FarmIDs[0] {
			return empty, ErrDependencyUnavailable
		}
		last = int64(rev)
		items = append(items, item)
	}
	if _, e = p.parcelActions(ctx, actor, ws, id, true); e != nil {
		return empty, e
	}
	next := ""
	if page.HasMore {
		if len(items) == 0 || page.NextAfterRevision != last {
			return empty, ErrDependencyUnavailable
		}
		cursor.AfterRevision = last
		next, e = p.signedCursor(cursor)
		if e != nil {
			return empty, e
		}
	}
	return farmListPage{Items: items, NextCursor: next, HasMore: page.HasMore}, nil
}

func (p *ParcelPlatform) directParcelIDs(ctx context.Context, actor, ws, farm string) ([]string, error) {
	ids := []string{}
	e := p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		if !roleAllowsAction(s.Roles, "agri.parcel.read") {
			return nil
		}
		var refs []model.AgricultureParcelReference
		if e := tx.Where("tenant_id=? AND workspace_id=? AND farm_id=?", s.Tenant.ID, ws, farm).Order("parcel_id").Find(&refs).Error; e != nil {
			return e
		}
		var bindings []model.AgricultureResourceBinding
		if e := tx.Where("tenant_id=? AND workspace_id=? AND subject_type='user' AND subject_id=? AND resource_kind='parcel' AND state='active' AND (expires_at IS NULL OR expires_at>?)", s.Tenant.ID, ws, actor, p.Service.now()).Find(&bindings).Error; e != nil {
			return e
		}
		allowed := map[string]bool{}
		for _, b := range bindings {
			caps, e := bindingCapabilities(b)
			if e != nil {
				return e
			}
			for _, cap := range caps {
				if cap == "agri.parcel.read" {
					allowed[b.ResourceID] = true
				}
			}
		}
		for _, r := range refs {
			if allowed[r.ParcelID] {
				ids = append(ids, r.ParcelID)
			}
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	active := []string{}
	for _, id := range ids {
		d, e := p.Service.Authorize(ctx, actor, ws, "agri.parcel.read", []ResourceRef{{"parcel", id}})
		if e == nil && len(d.FarmIDs) == 1 && d.FarmIDs[0] == farm {
			active = append(active, id)
		} else if e != nil && !errors.Is(e, ErrForbidden) && !errors.Is(e, ErrNotFound) && !errors.Is(e, ErrConflict) {
			return nil, e
		}
	}
	return active, nil
}

func (p *ParcelPlatform) Context(ctx context.Context, actor, ws, farm string) (map[string]any, error) {
	if !ownershipID.MatchString(farm) {
		return nil, ErrInvalid
	}
	if e := p.Service.withWorkspace(ctx, actor, ws, false, func(_ *gorm.DB, _ workspaceContext) error { return nil }); e != nil {
		return nil, e
	}
	caps := []string{}
	revision := int64(0)
	for _, action := range []string{"agri.parcel.create", "agri.parcel.read", "agri.parcel.update"} {
		d, e := p.Service.Authorize(ctx, actor, ws, action, []ResourceRef{{"farm", farm}})
		if e == nil && len(d.ResourceVersions) == 1 {
			caps = append(caps, action)
			revision = d.ResourceVersions[0].Revision
		} else if e != nil && !errors.Is(e, ErrForbidden) && !errors.Is(e, ErrNotFound) {
			return nil, e
		}
	}
	hasRead := false
	for _, cap := range caps {
		if cap == "agri.parcel.read" {
			hasRead = true
		}
	}
	if !hasRead {
		ids, e := p.directParcelIDs(ctx, actor, ws, farm)
		if e != nil {
			return nil, e
		}
		if len(ids) > 0 {
			caps = append(caps, "agri.parcel.read")
		}
	}
	if len(caps) == 0 {
		return nil, ErrNotFound
	}
	sort.Strings(caps)
	return map[string]any{"workspace_id": ws, "farm_id": farm, "expected_farm_revision": revision, "allowed_actions": caps}, nil
}

func (p *ParcelPlatform) List(ctx context.Context, actor, ws, farm string, limit int, token string) (farmListPage, error) {
	empty := farmListPage{Items: []any{}, NextCursor: "", HasMore: false}
	if !ownershipID.MatchString(farm) || limit < 1 || limit > 200 {
		return empty, ErrInvalid
	}
	cursor, e := p.cursor(ctx, actor, ws, "list", farm, token, 0)
	if e != nil {
		return empty, e
	}
	parent, parentErr := p.Service.Authorize(ctx, actor, ws, "agri.parcel.read", []ResourceRef{{"farm", farm}})
	if parentErr != nil && !errors.Is(parentErr, ErrForbidden) && !errors.Is(parentErr, ErrNotFound) {
		return empty, parentErr
	}
	refs := []ResourceRef{}
	mode := "farm"
	ids := []string{}
	if parentErr != nil {
		mode = "ids"
		ids, e = p.directParcelIDs(ctx, actor, ws, farm)
		if e != nil {
			return empty, e
		}
		for _, id := range ids {
			if id > cursor.AfterID {
				refs = append(refs, ResourceRef{"parcel", id})
			}
		}
		if len(refs) == 0 {
			return empty, nil
		}
	}
	items := []map[string]any{}
	hasMore := false
	nextID := ""
	for offset := 0; ; offset += 200 {
		decision := parent
		chunk := []string{}
		pageLimit := limit
		if mode == "ids" {
			if offset >= len(refs) {
				break
			}
			end := offset + 200
			if end > len(refs) {
				end = len(refs)
			}
			part := refs[offset:end]
			for _, r := range part {
				chunk = append(chunk, r.ID)
			}
			decision, e = p.Service.Authorize(ctx, actor, ws, "agri.parcel.read", part)
			if e != nil {
				return empty, e
			}
			pageLimit = 200
		}
		if len(decision.FarmIDs) != 1 || decision.FarmIDs[0] != farm {
			return empty, ErrForbidden
		}
		body, _, _ := farmCanonical(map[string]any{"access_mode": mode, "parcel_ids": chunk, "after_id": cursor.AfterID, "limit": pageLimit})
		raw, e := p.Client.farmAuthorized(ctx, p.Service, actor, decision, "POST", "/internal/agri/v1/farms/"+farm+"/parcels/list", body, flow.NewID("req_"))
		if e != nil {
			return empty, e
		}
		var page struct {
			Items       []json.RawMessage `json:"items"`
			NextAfterID string            `json:"next_after_id"`
			HasMore     bool              `json:"has_more"`
		}
		if strictFarmJSON(raw, &page, 1<<20) != nil || page.Items == nil || len(page.Items) > pageLimit {
			return empty, ErrDependencyUnavailable
		}
		lastID := cursor.AfterID
		allowedIDs := map[string]bool{}
		for _, id := range chunk {
			allowedIDs[id] = true
		}
		for _, encoded := range page.Items {
			item, e := parcelDecodeMap(encoded)
			if e != nil {
				return empty, e
			}
			id, ok := item["id"].(string)
			if !ok || !parcelIDPattern.MatchString(id) || item["farm_id"] != farm || item["workspace_id"] != ws || id <= lastID || (mode == "ids" && !allowedIDs[id]) {
				return empty, ErrDependencyUnavailable
			}
			lastID = id
			actions, e := p.parcelActions(ctx, actor, ws, id, false)
			if e != nil {
				return empty, e
			}
			item["allowed_actions"] = actions
			items = append(items, item)
		}
		if mode == "farm" {
			if page.HasMore && (len(page.Items) == 0 || page.NextAfterID != lastID) {
				return empty, ErrDependencyUnavailable
			}
			hasMore = page.HasMore
			nextID = page.NextAfterID
			break
		}
		if page.HasMore {
			return empty, ErrDependencyUnavailable
		}
		if len(items) > limit {
			hasMore = true
			items = items[:limit]
			nextID = items[len(items)-1]["id"].(string)
			break
		}
	}
	next := ""
	if hasMore {
		if !parcelIDPattern.MatchString(nextID) {
			return empty, ErrDependencyUnavailable
		}
		cursor.AfterID = nextID
		next, e = p.signedCursor(cursor)
		if e != nil {
			return empty, e
		}
	}
	return farmListPage{Items: items, NextCursor: next, HasMore: hasMore}, nil
}

// Parent discovery is an authorization index, not a farm content listing.
// Candidates come only from current scoped bindings/references and each is
// independently checked against current GIS ownership before exposure.
func (p *ParcelPlatform) Parents(ctx context.Context, actor, ws string, limit int, token string) (farmListPage, error) {
	empty := farmListPage{Items: []any{}, NextCursor: "", HasMore: false}
	if limit < 1 || limit > 200 {
		return empty, ErrInvalid
	}
	cursor, e := p.cursor(ctx, actor, ws, "parents", ws, token, 0)
	if e != nil {
		return empty, e
	}
	candidates := map[string]bool{}
	e = p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		var bindings []model.AgricultureResourceBinding
		if e := tx.Where("tenant_id=? AND workspace_id=? AND subject_type='user' AND subject_id=? AND state='active' AND (expires_at IS NULL OR expires_at>?)", s.Tenant.ID, ws, actor, p.Service.now()).Find(&bindings).Error; e != nil {
			return e
		}
		direct := []string{}
		for _, b := range bindings {
			caps, e := bindingCapabilities(b)
			if e != nil {
				return e
			}
			relevant := false
			for _, cap := range caps {
				if (cap == "agri.parcel.create" || cap == "agri.parcel.read" || cap == "agri.parcel.update") && roleAllowsAction(s.Roles, cap) {
					relevant = true
				}
			}
			if !relevant {
				continue
			}
			if b.ResourceKind == "farm" {
				candidates[b.ResourceID] = true
			} else if b.ResourceKind == "parcel" {
				direct = append(direct, b.ResourceID)
			}
		}
		if len(direct) > 0 {
			var refs []model.AgricultureParcelReference
			if e := tx.Where("tenant_id=? AND workspace_id=? AND parcel_id IN ?", s.Tenant.ID, ws, direct).Find(&refs).Error; e != nil {
				return e
			}
			for _, ref := range refs {
				candidates[ref.FarmID] = true
			}
		}
		return nil
	})
	if e != nil {
		return empty, e
	}
	ids := []string{}
	for id := range candidates {
		if id > cursor.AfterID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	items := []map[string]any{}
	next := ""
	more := false
	for _, id := range ids {
		item, e := p.Context(ctx, actor, ws, id)
		if errors.Is(e, ErrForbidden) || errors.Is(e, ErrNotFound) || errors.Is(e, ErrConflict) {
			continue
		}
		if e != nil {
			return empty, e
		}
		if len(items) == limit {
			more = true
			break
		}
		items = append(items, item)
	}
	if more {
		cursor.AfterID = items[len(items)-1]["farm_id"].(string)
		next, e = p.signedCursor(cursor)
		if e != nil {
			return empty, e
		}
	}
	return farmListPage{Items: items, NextCursor: next, HasMore: more}, nil
}

package agriculture

import (
	"context"
	"encoding/json"
	"gorm.io/gorm"
	"slices"
	"sort"
	"time"
	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

func (p *FarmPlatform) readableFarms(ctx context.Context, actor, ws string) ([]string, error) {
	ids := []string{}
	e := p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		if !roleAllowsAction(s.Roles, "agri.farm.read") {
			return nil
		}
		var rows []model.AgricultureResourceBinding
		if e := tx.Where("tenant_id=? AND workspace_id=? AND resource_kind='farm' AND subject_type='user' AND subject_id=? AND state='active' AND (expires_at IS NULL OR expires_at>?)", s.Tenant.ID, ws, actor, p.Service.now()).Find(&rows).Error; e != nil {
			return e
		}
		for _, b := range rows {
			caps, e := bindingCapabilities(b)
			if e != nil {
				return e
			}
			for _, cap := range caps {
				if cap == "agri.farm.read" {
					ids = append(ids, b.ResourceID)
					break
				}
			}
		}
		return nil
	})
	sort.Strings(ids)
	return ids, e
}
func (p *FarmPlatform) farmActions(ctx context.Context, actor, ws, id string) ([]string, error) {
	actions := []string{}
	e := p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		var b model.AgricultureResourceBinding
		if tx.Where("tenant_id=? AND workspace_id=? AND resource_kind='farm' AND resource_id=? AND subject_type='user' AND subject_id=? AND state='active' AND (expires_at IS NULL OR expires_at>?)", s.Tenant.ID, ws, id, actor, p.Service.now()).First(&b).Error != nil {
			return ErrNotFound
		}
		caps, e := bindingCapabilities(b)
		if e != nil {
			return e
		}
		for _, cap := range caps {
			if roleAllowsAction(s.Roles, cap) {
				switch cap {
				case "agri.farm.read":
					actions = append(actions, "agri.farm.read")
				case "agri.farm.update":
					actions = append(actions, "agri.farm.update")
				case "agri.farm.archive":
					actions = append(actions, "agri.farm.archive")
				}
			}
		}
		return nil
	})
	sort.Strings(actions)
	return actions, e
}
func (p *FarmPlatform) ReadFarm(ctx context.Context, actor, ws, id string) (map[string]any, error) {
	ids, e := p.readableFarms(ctx, actor, ws)
	if e != nil {
		return nil, e
	}
	found := false
	for _, x := range ids {
		if x == id {
			found = true
		}
	}
	if !found {
		return nil, ErrNotFound
	}
	body, _, _ := farmCanonical(map[string]string{"farm_id": id})
	raw, e := p.workspaceCall(ctx, actor, ws, "farm.history.read", flow.NewID("req_"), zeroFarmHash, zeroFarmHash, "/internal/agri/v1/farms/history", []string{id}, body)
	if e != nil {
		return nil, e
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil || result["id"] != id || result["workspace_id"] != ws {
		return nil, ErrDependencyUnavailable
	}
	actions, e := p.farmActions(ctx, actor, ws, id)
	if e != nil {
		return nil, e
	}
	if !slices.Contains(actions, "agri.farm.read") {
		return nil, ErrNotFound
	}
	if result["status"] == "archived" {
		actions = []string{"agri.farm.read"}
	}
	result["allowed_actions"] = actions
	return result, nil
}
func (p *FarmPlatform) ListFarms(ctx context.Context, actor, ws, status string, limit int, cursor, search string) (map[string]any, error) {
	if status != "active" && status != "archived" || limit < 1 || limit > 200 || len(search) > 200 {
		return nil, ErrInvalid
	}
	position, e := p.cursorScope(ctx, actor, ws, status, search, cursor)
	if e != nil {
		return nil, e
	}
	ids, e := p.readableFarms(ctx, actor, ws)
	if e != nil {
		return nil, e
	}
	eligible := []string{}
	for _, id := range ids {
		if id > position.AfterID {
			eligible = append(eligible, id)
		}
	}
	response := struct {
		Items   []map[string]any
		HasMore bool
	}{Items: []map[string]any{}}
	// Bound every signed workspace ticket to at most 200 explicit grants.
	// Scan sorted disjoint chunks rather than silently truncating large ACLs.
	for offset := 0; offset < len(eligible); offset += 200 {
		end := offset + 200
		if end > len(eligible) {
			end = len(eligible)
		}
		chunk := eligible[offset:end]
		body, _, _ := farmCanonical(map[string]any{"farm_ids": chunk, "status": status, "limit": 200, "after_id": "", "search": search})
		raw, err := p.workspaceCall(ctx, actor, ws, "farm.list", flow.NewID("req_"), zeroFarmHash, zeroFarmHash, "/internal/agri/v1/farms/list", chunk, body)
		if err != nil {
			return nil, err
		}
		var page struct {
			Items       []map[string]any `json:"items"`
			NextAfterID string           `json:"next_after_id"`
			HasMore     bool             `json:"has_more"`
		}
		if strictFarmJSON(raw, &page, 1<<20) != nil || page.Items == nil || len(page.Items) > 200 || page.HasMore {
			return nil, ErrDependencyUnavailable
		}
		response.Items = append(response.Items, page.Items...)
		if len(response.Items) > limit {
			response.HasMore = true
			response.Items = response.Items[:limit]
			break
		}
	}

	allowed := map[string]bool{}
	for _, id := range ids {
		allowed[id] = true
	}
	for _, item := range response.Items {
		id, ok := item["id"].(string)
		if !ok || !allowed[id] {
			return nil, ErrDependencyUnavailable
		}
		actions, e := p.farmActions(ctx, actor, ws, id)
		if e != nil {
			return nil, e
		}
		if !slices.Contains(actions, "agri.farm.read") {
			return nil, ErrNotFound
		}
		if status == "archived" {
			actions = []string{"agri.farm.read"}
		}
		item["allowed_actions"] = actions
	}
	next := ""
	if response.HasMore {
		last, ok := response.Items[len(response.Items)-1]["id"].(string)
		if !ok || !ownershipID.MatchString(last) {
			return nil, ErrDependencyUnavailable
		}
		position.AfterID = last
		next, e = p.Client.keys.sign(farmCursorType, position, p.Service.now(), time.Unix(position.ExpiresAt, 0))
		if e != nil {
			return nil, ErrDependencyUnavailable
		}
	}
	return map[string]any{"items": response.Items, "next_cursor": next, "has_more": response.HasMore}, nil
}
func (p *FarmPlatform) Context(ctx context.Context, actor, ws string) (map[string]any, error) {
	canCreate := false
	members := []map[string]any{}
	e := p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		canCreate = s.Owner || hasRole(s.Roles, "tenant_admin")
		if !canCreate {
			return nil
		}
		var memberships []model.WorkspaceMembership
		if e := tx.Where("workspace_id=?", ws).Find(&memberships).Error; e != nil {
			return e
		}
		for _, m := range memberships {
			target, e := resolve(tx, m.UserID, ws, false)
			if e != nil {
				continue
			}
			caps := []string{}
			for _, cap := range []string{"agri.farm.read", "agri.farm.update", "agri.farm.archive"} {
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
			members = append(members, map[string]any{"user_id": m.UserID, "display_name": user.Username, "roles": target.Roles, "allowed_capabilities": caps})
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	raw, e := p.workspaceCall(ctx, actor, ws, "farm.context", flow.NewID("req_"), zeroFarmHash, zeroFarmHash, "/internal/agri/v1/farms/context", []string{}, []byte("{}"))
	if e != nil {
		return nil, e
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil {
		return nil, ErrDependencyUnavailable
	}
	e = p.Service.withWorkspace(ctx, actor, ws, false, func(_ *gorm.DB, s workspaceContext) error {
		canCreate = s.Owner || hasRole(s.Roles, "tenant_admin")
		if !canCreate {
			members = []map[string]any{}
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	result["workspace_id"] = ws
	result["can_create"] = canCreate
	result["members"] = members
	return result, nil
}

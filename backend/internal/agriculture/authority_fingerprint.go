package agriculture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"gorm.io/gorm"
	"yingce/backend/internal/model"
)

// authorityFingerprint is a UI invalidation hint over platform authority INPUTS,
// never an authorization decision. It neither calls GIS nor replaces the live
// actor/membership/ownership/epoch checks on every actual operation.
func authorityFingerprint(tx *gorm.DB, scope workspaceContext, actorID string, now time.Time) (string, error) {
	var rows []model.AgricultureResourceBinding
	if err := tx.Where("tenant_id=? AND workspace_id=? AND subject_type='user' AND subject_id=? AND state='active' AND (expires_at IS NULL OR expires_at>?)", scope.Tenant.ID, scope.Workspace.ID, actorID, now).Find(&rows).Error; err != nil {
		return "", err
	}
	type grant struct {
		Kind            string   `json:"kind"`
		ID              string   `json:"id"`
		Capabilities    []string `json:"capabilities"`
		Expires         string   `json:"expires"`
		ReceiptHash     string   `json:"receiptHash"`
		ReceiptRevision int64    `json:"receiptRevision"`
	}
	grants := make([]grant, 0, len(rows))
	for _, row := range rows {
		caps, err := bindingCapabilities(row)
		if err != nil {
			return "", err
		}
		sort.Strings(caps)
		expires := ""
		if row.ExpiresAt != nil {
			expires = row.ExpiresAt.UTC().Format(time.RFC3339Nano)
		}
		grants = append(grants, grant{row.ResourceKind, row.ResourceID, caps, expires, row.ReceiptHash, row.ReceiptRevision})
	}
	// Sort the complete canonical grant representation rather than database IDs
	// or update times. Recreating an equivalent binding must not destroy a draft.
	sort.Slice(grants, func(i, j int) bool {
		a, _ := json.Marshal(grants[i])
		b, _ := json.Marshal(grants[j])
		return string(a) < string(b)
	})
	roles := append([]string{}, scope.Roles...)
	sort.Strings(roles)
	projection := view(scope)
	payload := struct {
		Version               string   `json:"version"`
		Policy                string   `json:"policy"`
		Actor                 string   `json:"actor"`
		Tenant                string   `json:"tenant"`
		Workspace             string   `json:"workspace"`
		TenantStatus          string   `json:"tenantStatus"`
		Owner                 bool     `json:"owner"`
		Roles                 []string `json:"roles"`
		CanManageMembers      bool     `json:"canManageMembers"`
		CanManageOrganization bool     `json:"canManageOrganization"`
		Grants                []grant  `json:"grants"`
	}{"platform-authority-inputs-v1", authorizationPolicyVersion, actorID, scope.Tenant.ID, scope.Workspace.ID, scope.Tenant.Status, scope.Owner, roles, projection.CanManageMembers, projection.CanManageOrganization, grants}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

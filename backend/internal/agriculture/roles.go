package agriculture

import "sort"

const Product = "agriculture"

var AssignableRoles = []string{"viewer", "analyst", "editor", "field_operator", "reviewer", "farm_manager", "tenant_admin", "billing_admin"}

// These are product roles, not Loom scopes. Governance never implies content
// access; farm/resource authorization is a separate, mandatory later boundary.
func normalizeRoles(input []string) ([]string, error) {
	if len(input) == 0 || len(input) > len(AssignableRoles) {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	for _, role := range input {
		switch role {
		case "admin":
			role = "tenant_admin"
		case "field_worker":
			role = "field_operator"
		}
		valid := false
		for _, allowed := range AssignableRoles {
			if role == allowed {
				valid = true
				break
			}
		}
		if !valid || seen[role] {
			return nil, ErrInvalid
		}
		seen[role] = true
	}
	out := make([]string, 0, len(seen))
	for role := range seen {
		out = append(out, role)
	}
	sort.Strings(out)
	return out, nil
}

func governanceRole(role string) bool { return role == "tenant_admin" || role == "billing_admin" }

func hasRole(roles []string, role string) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

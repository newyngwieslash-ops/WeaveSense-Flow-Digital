package agriculture

import (
	"regexp"
	"strings"
	"testing"
)

// Independent expected matrix: each cell is checked, including every denial.
var policyTestActions = []string{
	"agri.farm.read", "agri.farm.update",
	"agri.parcel.read", "agri.parcel.create", "agri.parcel.update", "agri.parcel.delete",
	"agri.asset.read", "agri.asset.create", "agri.asset_download.read",
	"agri.analysis.read", "agri.analysis.submit", "agri.analysis.cancel",
	"agri.inspection.read", "agri.inspection.create", "agri.inspection.update", "agri.inspection.submit",
	"agri.report.read", "agri.report.create", "agri.report.update", "agri.report.review", "agri.report.publish",
	"agri.authorization.read", "agri.authorization.update", "agri.billing.read", "agri.billing.update",
}

func TestRoleActionCeilingMatrix(t *testing.T) {
	rows := map[string]string{
		"viewer":         "1010001011001000100000000",
		"analyst":        "1010001111111100111000000",
		"editor":         "1011111111111111111000000",
		"field_operator": "1010000000001111000000000",
		"reviewer":       "1010001011001000100110000",
		"farm_manager":   "1111111111111111111111100",
		"tenant_admin":   "0000000000000000000001100",
		"billing_admin":  "0000000000000000000000011",
	}
	for role, expected := range rows {
		if len(expected) != len(policyTestActions) {
			t.Fatalf("invalid matrix width for %s", role)
		}
		for i, action := range policyTestActions {
			t.Run(role+"/"+action, func(t *testing.T) {
				if got, want := roleAllowsAction([]string{role}, action), expected[i] == '1'; got != want {
					t.Fatalf("ceiling = %v, want %v", got, want)
				}
			})
		}
	}
}

func TestPolicyCatalogClosedAndDelegationSyntax(t *testing.T) {
	// Keep exact scopes compatible with the current delegation schema.
	pattern := regexp.MustCompile(`^agri\.[a-z_]+\.(read|create|update|delete|submit|cancel|publish|review|claim)$`)
	seen := map[string]bool{}
	for _, action := range policyTestActions {
		if !knownAction(action) || !pattern.MatchString(action) || seen[action] {
			t.Fatalf("invalid/duplicate catalog entry %q", action)
		}
		seen[action] = true
	}
	for _, action := range []string{"", "*", "agri.*.read", "agri.asset.upload", "agri.farm.create", "agri.inspection.claim", "agri.report.delete", "agri.billing.refund", "AGRI.farm.read", " agri.farm.read", "agri.farm.read\n", "agri.farm.read\x00"} {
		if knownAction(action) || roleAllowsAction([]string{"farm_manager", "billing_admin"}, action) {
			t.Errorf("unknown action accepted: %q", action)
		}
	}
}

func TestPolicyUnknownAndAliasRolesFailClosed(t *testing.T) {
	for _, role := range []string{"", "admin", "field_worker", "owner", "system", "superadmin", "Viewer", "viewer ", "viewer\x00", "*"} {
		for _, roles := range [][]string{{role}, {role, "farm_manager"}, {"farm_manager", role}} {
			for _, action := range policyTestActions {
				if roleAllowsAction(roles, action) {
					t.Errorf("invalid mixed role input %q accepted for %s", roles, action)
				}
			}
		}
	}
	for _, roles := range [][]string{nil, {}} {
		for _, action := range policyTestActions {
			if roleAllowsAction(roles, action) {
				t.Errorf("empty roles accepted for %s", action)
			}
		}
	}
}

func TestPolicyCanonicalRoleCombinationIsUnionNotPrivilegeExpansion(t *testing.T) {
	roles := []string{"viewer", "analyst", "editor", "field_operator", "reviewer", "farm_manager", "tenant_admin", "billing_admin"}
	for _, first := range roles {
		for _, second := range roles {
			for _, action := range policyTestActions {
				want := roleAllowsAction([]string{first}, action) || roleAllowsAction([]string{second}, action)
				if got := roleAllowsAction([]string{first, second}, action); got != want {
					t.Fatalf("combination %s + %s expanded %s", first, second, action)
				}
			}
		}
	}
	for _, action := range policyTestActions {
		if !strings.HasPrefix(action, "agri.authorization.") && !strings.HasPrefix(action, "agri.billing.") && roleAllowsAction([]string{"tenant_admin", "billing_admin"}, action) {
			t.Fatalf("combined governance roles acquired content ceiling: %s", action)
		}
	}
}

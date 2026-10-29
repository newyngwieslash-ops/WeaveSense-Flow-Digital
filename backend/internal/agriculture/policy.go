package agriculture

// knownAction is a closed catalog, not the delegation schema's broader syntax.
// Adding a scope requires a policy decision; a syntactically valid scope alone
// must never acquire authority.
func knownAction(action string) bool {
	switch action {
	case "agri.farm.read", "agri.farm.update",
		"agri.parcel.read", "agri.parcel.create", "agri.parcel.update", "agri.parcel.delete",
		"agri.asset.read", "agri.asset.create", "agri.asset_download.read",
		"agri.analysis.read", "agri.analysis.submit", "agri.analysis.cancel",
		"agri.inspection.read", "agri.inspection.create", "agri.inspection.update", "agri.inspection.submit",
		"agri.report.read", "agri.report.create", "agri.report.update", "agri.report.review", "agri.report.publish",
		"agri.authorization.read", "agri.authorization.update",
		"agri.billing.read", "agri.billing.update":
		return true
	default:
		return false
	}
}

// roleAllowsAction is only a capability CEILING, never an authorization grant.
// Every content action also requires an exact, explicit resource capability,
// current actor/membership, GIS-confirmed ownership and domain-state checks.
// In particular downloads, analyst upload, editor analysis and manager review /
// publish are optional capabilities, not defaults derived from these roles.
// There is no owner role here: organization ownership conveys no content rights.
func roleAllowsAction(roles []string, action string) bool {
	if !knownAction(action) || len(roles) == 0 {
		return false
	}
	// Validate the entire input before combining ceilings. Never accept aliases
	// at this boundary, nor let a known role mask a corrupt/unknown role.
	for _, role := range roles {
		switch role {
		case "viewer", "analyst", "editor", "field_operator", "reviewer", "farm_manager", "tenant_admin", "billing_admin":
		default:
			return false
		}
	}
	for _, role := range roles {
		if singleRoleAllowsAction(role, action) {
			return true
		}
	}
	return false
}

func singleRoleAllowsAction(role, action string) bool {
	switch role {
	case "tenant_admin":
		return action == "agri.authorization.read" || action == "agri.authorization.update"
	case "billing_admin":
		return action == "agri.billing.read" || action == "agri.billing.update"
	case "field_operator":
		// Domain checks must restrict these to assigned tasks and minimal data.
		// General asset.create does not represent restricted field-media upload.
		switch action {
		case "agri.farm.read", "agri.parcel.read", "agri.inspection.read", "agri.inspection.create", "agri.inspection.update", "agri.inspection.submit":
			return true
		}
		return false
	}

	// Readers see only the projection permitted by domain policy (for example,
	// viewer report.read is published-only). Download remains a separate grant.
	switch action {
	case "agri.farm.read", "agri.parcel.read", "agri.asset.read", "agri.asset_download.read", "agri.analysis.read", "agri.inspection.read", "agri.report.read":
		return role == "viewer" || role == "analyst" || role == "editor" || role == "reviewer" || role == "farm_manager"
	}
	switch role {
	case "analyst":
		switch action {
		case "agri.asset.create", "agri.analysis.submit", "agri.analysis.cancel", "agri.inspection.create", "agri.report.create", "agri.report.update":
			return true
		}
	case "editor":
		switch action {
		case "agri.parcel.create", "agri.parcel.update", "agri.parcel.delete", "agri.asset.create", "agri.analysis.submit", "agri.analysis.cancel", "agri.inspection.create", "agri.inspection.update", "agri.inspection.submit", "agri.report.create", "agri.report.update":
			return true
		}
	case "reviewer":
		// Independent review and publication state are mandatory domain checks.
		// Evidence upload needs a narrower action than general asset.create.
		return action == "agri.report.review" || action == "agri.report.publish"
	case "farm_manager":
		// Governance is limited to explicitly granted farms, never tenant-wide.
		// Usage/budget projections need narrower actions than general billing.
		switch action {
		case "agri.farm.update", "agri.parcel.create", "agri.parcel.update", "agri.parcel.delete", "agri.asset.create", "agri.analysis.submit", "agri.analysis.cancel", "agri.inspection.create", "agri.inspection.update", "agri.inspection.submit", "agri.report.create", "agri.report.update", "agri.report.review", "agri.report.publish", "agri.authorization.read", "agri.authorization.update":
			return true
		}
	}
	return false
}

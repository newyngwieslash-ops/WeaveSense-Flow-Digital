package agriculture

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"time"
)

var ErrDependencyUnavailable = errors.New("agriculture: ownership dependency unavailable")

// ResourceRef identifies a domain object, never a caller or an authorization grant.
type ResourceRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// OwnershipContext must be constructed from the verified platform session and
// authorized workspace. Browser and tool arguments are not trusted contexts.
type OwnershipContext struct {
	TenantID    string
	WorkspaceID string
}

// ResourceOwnership is a short-lived receipt from the domain owner. It is not a
// public API claim and does not itself grant any business capability.
type ResourceOwnership struct {
	Resource        ResourceRef `json:"resource"`
	TenantID        string      `json:"tenantId"`
	WorkspaceID     string      `json:"workspaceId"`
	FarmIDs         []string    `json:"farmIds"`
	Revision        int64       `json:"revision"`
	Status          string      `json:"status"`
	ReceiptID       string      `json:"receiptId"`
	IssuedAt        time.Time   `json:"issuedAt"`
	ExpiresAt       time.Time   `json:"expiresAt"`
	AssignedActorID string      `json:"assignedActorId,omitempty"`
}

// OwnershipResolver is an internal dependency boundary. Implementations must
// authenticate the owning service; JSON shape validation is not verification of
// its signature or transport. No production GIS resolver is provided here.
type OwnershipResolver interface {
	Resolve(context.Context, OwnershipContext, []ResourceRef) ([]ResourceOwnership, error)
}

// Matches contracts/agriculture/common.schema.json's resource identifier.
var ownershipID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,127}$`)

const maxOwnershipRevision int64 = 1<<53 - 1

func canonicalResources(resources []ResourceRef) ([]ResourceRef, error) {
	if len(resources) == 0 || len(resources) > 200 {
		return nil, ErrInvalid
	}
	out := make([]ResourceRef, len(resources))
	seen := make(map[ResourceRef]bool, len(resources))
	for i, resource := range resources {
		switch resource.Kind {
		case "farm", "parcel", "asset", "job", "report":
		default:
			return nil, ErrInvalid
		}
		if !ownershipID.MatchString(resource.ID) || seen[resource] {
			return nil, ErrInvalid
		}
		seen[resource] = true
		out[i] = resource
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func validateOwnership(now time.Time, scope OwnershipContext, requested []ResourceRef, proofs []ResourceOwnership) (map[ResourceRef]ResourceOwnership, error) {
	resources, err := canonicalResources(requested)
	if err != nil {
		return nil, err
	}
	if now.IsZero() || !ownershipID.MatchString(scope.TenantID) || !ownershipID.MatchString(scope.WorkspaceID) || len(proofs) != len(resources) {
		return nil, ErrDependencyUnavailable
	}
	// Validate the complete response set before accepting any receipt. A partial
	// response must never result in partial authorization of a batch operation.
	remaining := make(map[ResourceRef]bool, len(resources))
	for _, resource := range resources {
		remaining[resource] = true
	}
	for _, proof := range proofs {
		if !remaining[proof.Resource] {
			return nil, ErrDependencyUnavailable
		}
		delete(remaining, proof.Resource)
	}
	out := make(map[ResourceRef]ResourceOwnership, len(resources))
	for _, proof := range proofs {
		if !ownershipID.MatchString(proof.TenantID) || !ownershipID.MatchString(proof.WorkspaceID) || proof.Status == "" {
			return nil, ErrDependencyUnavailable
		}
		if proof.TenantID != scope.TenantID || proof.WorkspaceID != scope.WorkspaceID || proof.Status != "active" {
			return nil, ErrNotFound
		}
		if proof.Revision <= 0 || proof.Revision > maxOwnershipRevision || !ownershipID.MatchString(proof.ReceiptID) ||
			proof.IssuedAt.IsZero() || proof.ExpiresAt.IsZero() || proof.IssuedAt.After(now.Add(5*time.Second)) ||
			proof.IssuedAt.Before(now.Add(-60*time.Second)) || !proof.ExpiresAt.After(now) ||
			!proof.ExpiresAt.After(proof.IssuedAt) || proof.ExpiresAt.Sub(proof.IssuedAt) > 60*time.Second ||
			len(proof.FarmIDs) == 0 || len(proof.FarmIDs) > 200 {
			return nil, ErrDependencyUnavailable
		}
		farms := make([]string, len(proof.FarmIDs))
		seen := make(map[string]bool, len(farms))
		for i, farm := range proof.FarmIDs {
			if !ownershipID.MatchString(farm) || seen[farm] {
				return nil, ErrDependencyUnavailable
			}
			seen[farm] = true
			farms[i] = farm
		}
		if proof.Resource.Kind == "farm" && (len(farms) != 1 || farms[0] != proof.Resource.ID) {
			return nil, ErrDependencyUnavailable
		}
		sort.Strings(farms)
		proof.FarmIDs = farms
		out[proof.Resource] = proof
	}
	return out, nil
}

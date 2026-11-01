package agriculture

// Job authority is durable metadata, not a queue, bearer token or lease. GIS owns
// persistence/claim/fencing. No browser route accepts or returns service tickets.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	"yingce/backend/internal/agricanonical"
	"yingce/backend/internal/flow"
)

const jobAuthoritySchema = "agriculture-job-authority/1"

type JobAuthoritySnapshot struct {
	SchemaVersion        string            `json:"schema_version"`
	JobID                string            `json:"job_id"`
	OperationID          string            `json:"operation_id"`
	ActorUserID          string            `json:"actor_user_id"`
	TenantID             string            `json:"tenant_id"`
	WorkspaceID          string            `json:"workspace_id"`
	Action               string            `json:"action"`
	Resources            []ResourceRef     `json:"resources"`
	ResourceVersions     []ResourceVersion `json:"resource_versions"`
	FarmIDs              []string          `json:"farm_ids"`
	AuthorizationVersion int64             `json:"authorization_version"`
	PolicyVersion        string            `json:"policy_version"`
	InputHash            string            `json:"input_hash"`
	CreatedAt            string            `json:"created_at"`
}

// NewJobAuthority is called only by a trusted platform producer after domain
// parameters are validated. Actor is supplied by its authenticated invocation,
// never copied from input. Input JSON contributes a hash, not identity/authority.
func (s Service) NewJobAuthority(ctx context.Context, actorID, workspaceID, operationID string, resources []ResourceRef, input []byte) (JobAuthoritySnapshot, AuthorizationDecision, error) {
	if !ownershipID.MatchString(operationID) {
		return JobAuthoritySnapshot{}, AuthorizationDecision{}, ErrInvalid
	}
	canonical, err := agricanonical.Canonicalize(input)
	if err != nil {
		return JobAuthoritySnapshot{}, AuthorizationDecision{}, ErrInvalid
	}
	decision, err := s.Authorize(ctx, actorID, workspaceID, "agri.analysis.submit", resources)
	if err != nil {
		return JobAuthoritySnapshot{}, AuthorizationDecision{}, err
	}
	hash := sha256.Sum256(canonical)
	snapshot := JobAuthoritySnapshot{
		SchemaVersion: jobAuthoritySchema, JobID: flow.NewID("job_"), OperationID: operationID,
		ActorUserID: decision.ActorID, TenantID: decision.TenantID, WorkspaceID: decision.WorkspaceID,
		Action: decision.Action, Resources: slices.Clone(decision.Resources), ResourceVersions: slices.Clone(decision.ResourceVersions),
		FarmIDs: slices.Clone(decision.FarmIDs), AuthorizationVersion: decision.AuthorizationVersion,
		PolicyVersion: decision.PolicyVersion, InputHash: hex.EncodeToString(hash[:]), CreatedAt: s.now().UTC().Truncate(time.Second).Format(time.RFC3339),
	}
	return snapshot, decision, nil
}

func (snapshot JobAuthoritySnapshot) canonical() ([]byte, error) {
	refs, err := canonicalResources(snapshot.Resources)
	created, timeErr := time.Parse(time.RFC3339, snapshot.CreatedAt)
	hash, hashErr := hex.DecodeString(snapshot.InputHash)
	if err != nil || !slices.Equal(refs, snapshot.Resources) || timeErr != nil || created.UTC().Format(time.RFC3339) != snapshot.CreatedAt ||
		snapshot.SchemaVersion != jobAuthoritySchema || !ownershipID.MatchString(snapshot.JobID) || !ownershipID.MatchString(snapshot.OperationID) ||
		!identityActorID.MatchString(snapshot.ActorUserID) || !ownershipID.MatchString(snapshot.TenantID) || !ownershipID.MatchString(snapshot.WorkspaceID) ||
		snapshot.Action != "agri.analysis.submit" || snapshot.PolicyVersion != authorizationPolicyVersion || snapshot.AuthorizationVersion < 1 || snapshot.AuthorizationVersion > maximumAuthorizationVersion ||
		hashErr != nil || len(hash) != 32 || hex.EncodeToString(hash) != snapshot.InputHash || len(snapshot.ResourceVersions) != len(refs) || len(snapshot.FarmIDs) < 1 || len(snapshot.FarmIDs) > 200 {
		return nil, ErrInvalid
	}
	for i, ref := range refs {
		v := snapshot.ResourceVersions[i]
		if v.Resource != ref || v.Revision < 1 || v.Revision > maximumAuthorizationVersion {
			return nil, ErrInvalid
		}
	}
	for i, farm := range snapshot.FarmIDs {
		if !ownershipID.MatchString(farm) || (i > 0 && snapshot.FarmIDs[i-1] >= farm) {
			return nil, ErrInvalid
		}
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, ErrInvalid
	}
	return agricanonical.Canonicalize(raw)
}

func (snapshot JobAuthoritySnapshot) Digest() (string, error) {
	canonical, err := snapshot.canonical()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(canonical)
	return hex.EncodeToString(hash[:]), nil
}

// CheckJobAuthority must receive the immutable record read by the authenticated
// GIS job registry, not a worker's message/body. Unlike Reauthorize it does NOT
// reuse the old 30-second request lifetime: current capability/expiry/ownership
// are independently recomputed. Conservative policy: any authority epoch,
// resource revision/farm or policy change rejects this job; retry cannot expand it.
func (s Service) CheckJobAuthority(ctx context.Context, snapshot JobAuthoritySnapshot, expectedHash string) (AuthorizationDecision, error) {
	digest, err := snapshot.Digest()
	if err != nil || digest != expectedHash {
		return AuthorizationDecision{}, ErrInvalid
	}
	created, _ := time.Parse(time.RFC3339, snapshot.CreatedAt)
	if created.After(s.now().Add(5 * time.Second)) {
		return AuthorizationDecision{}, ErrInvalid
	}
	next, err := s.Authorize(ctx, snapshot.ActorUserID, snapshot.WorkspaceID, snapshot.Action, snapshot.Resources)
	if err != nil {
		return AuthorizationDecision{}, err
	}
	if next.TenantID != snapshot.TenantID || next.AuthorizationVersion != snapshot.AuthorizationVersion ||
		next.PolicyVersion != snapshot.PolicyVersion || !slices.Equal(next.Resources, snapshot.Resources) ||
		!slices.Equal(next.ResourceVersions, snapshot.ResourceVersions) || !slices.Equal(next.FarmIDs, snapshot.FarmIDs) {
		return AuthorizationDecision{}, ErrConflict
	}
	return next, nil
}

package agriculture

import (
	"context"
	"encoding/json"
)

const jobAcceptPath = "/internal/agri/v1/jobs/authority/accept"
const jobCheckpointPath = "/internal/agri/v1/jobs/authority/checkpoint"

type JobCheckpoint struct {
	JobID        string `json:"job_id"`
	SnapshotHash string `json:"snapshot_hash"`
	WorkerID     string `json:"worker_id"`
	Attempt      int64  `json:"attempt"`
	Fencing      int64  `json:"fencing"`
	Phase        string `json:"phase"`
	InputHash    string `json:"input_hash"`
}

// RegisterJobAuthority consumes only a server-created snapshot and validated
// domain input. It is not a browser/API endpoint and does not enqueue a job.
func (g *GISClient) RegisterJobAuthority(ctx context.Context, s Service, snapshot JobAuthoritySnapshot, input []byte) ([]byte, error) {
	digest, err := snapshot.Digest()
	if err != nil {
		return nil, err
	}
	decision, err := s.CheckJobAuthority(ctx, snapshot, digest)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(struct {
		Snapshot JobAuthoritySnapshot `json:"snapshot"`
		Input    json.RawMessage      `json:"input"`
	}{snapshot, json.RawMessage(input)})
	if err != nil {
		return nil, ErrInvalid
	}
	return g.CallAuthorized(ctx, s, snapshot.ActorUserID, decision, "POST", jobAcceptPath, body, snapshot.OperationID)
}

// CheckJobExecution is callable only by a trusted dispatcher holding a loaded
// immutable snapshot. A worker/browser request must never select this snapshot
// or impersonate its actor. GIS independently checks the persisted queue lease.
func (g *GISClient) CheckJobExecution(ctx context.Context, s Service, snapshot JobAuthoritySnapshot, checkpoint JobCheckpoint) ([]byte, error) {
	digest, err := snapshot.Digest()
	if err != nil || checkpoint.JobID != snapshot.JobID || checkpoint.SnapshotHash != digest || checkpoint.InputHash != snapshot.InputHash || !ownershipID.MatchString(checkpoint.WorkerID) || checkpoint.Attempt < 1 || checkpoint.Attempt > maximumAuthorizationVersion || checkpoint.Fencing < 1 || checkpoint.Fencing > maximumAuthorizationVersion || (checkpoint.Phase != "claim" && checkpoint.Phase != "read_input" && checkpoint.Phase != "publish") {
		return nil, ErrInvalid
	}
	decision, err := s.CheckJobAuthority(ctx, snapshot, digest)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(checkpoint)
	if err != nil {
		return nil, ErrInvalid
	}
	return g.CallAuthorized(ctx, s, snapshot.ActorUserID, decision, "POST", jobCheckpointPath, body, snapshot.OperationID)
}

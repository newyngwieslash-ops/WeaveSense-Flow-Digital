package agriculture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"yingce/backend/internal/agricanonical"
	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

const farmWorkspaceType = "weavesense-agri-workspace-operation+jws"
const farmAuthorityPath = "/internal/agri/v1/farms/authority/check"
const zeroFarmHash = "0000000000000000000000000000000000000000000000000000000000000000"

var ErrFarmGeometry = errors.New("agriculture: invalid farm geometry")

type FarmGrant struct {
	SubjectUserID string     `json:"subject_user_id"`
	Capabilities  []string   `json:"capabilities"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}
type FarmPayload struct {
	Action           string          `json:"action"`
	FarmID           string          `json:"farm_id,omitempty"`
	ExpectedRevision int64           `json:"expected_revision,omitempty"`
	Farm             json.RawMessage `json:"farm,omitempty"`
	InitialGrants    []FarmGrant     `json:"initial_grants,omitempty"`
	Reason           string          `json:"reason,omitempty"`
}
type FarmPlatform struct {
	Service    Service
	Client     *GISClient
	Dispatcher flow.CommandDispatcher
}

func NewFarmPlatform(s Service, g *GISClient) *FarmPlatform {
	p := &FarmPlatform{Service: s, Client: g}
	p.Dispatcher = flow.CommandDispatcher{DB: s.DB, Adapters: map[string]flow.CommandAdapter{"agri.farm.create": p, "agri.farm.update": p, "agri.farm.archive": p}}
	return p
}

type FarmReceipt struct {
	SchemaVersion     string `json:"schema_version"`
	Producer          string `json:"producer"`
	OperationID       string `json:"operation_id"`
	TenantID          string `json:"tenant_id"`
	WorkspaceID       string `json:"workspace_id"`
	RequestHash       string `json:"request_hash"`
	GrantPlanHash     string `json:"grant_plan_hash"`
	FarmID            string `json:"farm_id"`
	Revision          int64  `json:"revision"`
	AggregateRevision int64  `json:"aggregate_revision"`
	EventID           string `json:"event_id"`
	EventType         string `json:"event_type"`
	PayloadHash       string `json:"payload_hash"`
	State             string `json:"state"`
}
type FarmOperationView struct {
	OperationID    string   `json:"operation_id"`
	Action         string   `json:"action"`
	State          string   `json:"state"`
	FarmID         string   `json:"farm_id"`
	Revision       int64    `json:"revision"`
	ErrorCode      string   `json:"error_code"`
	AllowedActions []string `json:"allowed_actions"`
	JobID          *string  `json:"job_id"`
	StatusURL      string   `json:"status_url"`
	EventsURL      *string  `json:"events_url"`
}

func farmOperationView(op model.AgricultureFarmOperation) FarmOperationView {
	return FarmOperationView{OperationID: op.ID, Action: op.Action, State: op.State, FarmID: op.FarmID, Revision: op.ResultRevision, ErrorCode: op.ErrorCode, AllowedActions: []string{}, StatusURL: "/api/agri/v1/farm-operations/" + op.ID + "?workspace_id=" + op.WorkspaceID}
}
func farmCanonical(v any) ([]byte, string, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return nil, "", ErrInvalid
	}
	raw, e = agricanonical.Canonicalize(raw)
	if e != nil {
		return nil, "", ErrInvalid
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}
func validateFarmPayload(v *FarmPayload) error {
	switch v.Action {
	case "create":
		if v.FarmID != "" || v.ExpectedRevision != 0 || v.Reason != "" || len(v.InitialGrants) < 1 || len(v.InitialGrants) > 20 {
			return ErrInvalid
		}
	case "update":
		if !ownershipID.MatchString(v.FarmID) || len(v.InitialGrants) > 0 || v.Reason != "" {
			return ErrInvalid
		}
		if v.ExpectedRevision < 1 {
			return ErrPreconditionRequired
		}
	case "archive":
		if !ownershipID.MatchString(v.FarmID) || len(v.InitialGrants) > 0 || len(v.Farm) > 0 || len(v.Reason) < 1 || len(v.Reason) > 500 {
			return ErrInvalid
		}
		if v.ExpectedRevision < 1 {
			return ErrPreconditionRequired
		}
	default:
		return ErrInvalid
	}
	if v.Action != "archive" {
		var fields map[string]json.RawMessage
		if len(v.Farm) == 0 || json.Unmarshal(v.Farm, &fields) != nil || fields == nil {
			return ErrInvalid
		}
		allowed := map[string]bool{"external_code": true, "name": true, "operator_name": true, "contact": true, "region_code": true, "dictionary_version": true, "timezone": true, "area_unit": true, "geometry": true, "source_crs": true, "coordinate_order": true, "data_class": true}
		for k, value := range fields {
			if !allowed[k] || string(value) == "null" {
				return ErrInvalid
			}
			if v.Action == "update" && (k == "external_code" || k == "data_class") {
				return ErrInvalid
			}
		}
	}
	seen := map[string]bool{}
	for i := range v.InitialGrants {
		grant := &v.InitialGrants[i]
		if !identityActorID.MatchString(grant.SubjectUserID) || seen[grant.SubjectUserID] || len(grant.Capabilities) < 1 || len(grant.Capabilities) > 3 {
			return ErrInvalid
		}
		seen[grant.SubjectUserID] = true
		caps := map[string]bool{}
		for _, c := range grant.Capabilities {
			if (c != "agri.farm.read" && c != "agri.farm.update" && c != "agri.farm.archive") || caps[c] {
				return ErrInvalid
			}
			caps[c] = true
		}
		sort.Strings(grant.Capabilities)
	}
	sort.Slice(v.InitialGrants, func(i, j int) bool { return v.InitialGrants[i].SubjectUserID < v.InitialGrants[j].SubjectUserID })
	return nil
}
func farmPayload(op model.AgricultureFarmOperation) (FarmPayload, error) {
	var v FarmPayload
	if strictFarmJSON([]byte(op.Payload), &v, 1<<20) != nil {
		return v, ErrInvalid
	}
	return v, validateFarmPayload(&v)
}
func farmCommandBody(op model.AgricultureFarmOperation, receipt bool) ([]byte, error) {
	v, e := farmPayload(op)
	if e != nil {
		return nil, e
	}
	body := map[string]any{"operation_id": op.ID, "request_hash": op.RequestHash}
	if v.Action == "create" || receipt {
		body["grant_plan_hash"] = op.GrantPlanHash
	}
	if !receipt {
		if v.Action != "archive" {
			body["farm"] = v.Farm
		}
		if v.Action != "create" {
			body["expected_revision"] = v.ExpectedRevision
		}
		if v.Action == "archive" {
			body["reason"] = v.Reason
		}
	}
	raw, _, e := farmCanonical(body)
	return raw, e
}

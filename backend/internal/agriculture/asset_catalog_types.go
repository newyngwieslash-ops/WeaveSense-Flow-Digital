package agriculture

import (
	"encoding/json"
	"regexp"
	"sort"
	"time"
	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

const assetCatalogType = "weavesense-agri-asset-operation+jws"
const assetCatalogCursorType = "weavesense-agri-asset-cursor+jws"
const assetCatalogAuthorityPath = "/internal/agri/v1/assets/authority/check"
const assetCatalogOwnershipPath = "/internal/agri/v1/assets/ownership/resolve"

var assetCatalogID = regexp.MustCompile(`^asset_[0-9a-f]{32}$`)
var assetCatalogVersionID = regexp.MustCompile(`^av_[0-9a-f]{32}$`)
var assetCatalogPaths = map[string]string{"asset.context": "/internal/agri/v1/assets/context", "asset.preview.create": "/internal/agri/v1/assets/preview-create", "asset.preview.version": "/internal/agri/v1/assets/preview-version", "asset.create": "/internal/agri/v1/assets/commands/create", "asset.version": "/internal/agri/v1/assets/commands/version", "asset.receipt": "/internal/agri/v1/assets/commands/receipt", "asset.list": "/internal/agri/v1/assets/list", "asset.read": "/internal/agri/v1/assets/read"}

type AssetCatalogPlatform struct {
	Service    Service
	Client     *GISClient
	Dispatcher flow.CommandDispatcher
}

func NewAssetCatalogPlatform(s Service, g *GISClient) *AssetCatalogPlatform {
	p := &AssetCatalogPlatform{Service: s, Client: g}
	p.Dispatcher = flow.CommandDispatcher{DB: s.DB, Now: s.Now, Adapters: map[string]flow.CommandAdapter{"agri.asset.create": p, "agri.asset.version": p}}
	return p
}

type AssetCatalogPayload struct {
	Action           string          `json:"action"`
	AssetID          string          `json:"asset_id,omitempty"`
	ExpectedRevision *int64          `json:"expected_revision"`
	Metadata         json.RawMessage `json:"metadata"`
	InitialGrants    []FarmGrant     `json:"initial_grants,omitempty"`
}
type AssetCatalogOperationView struct {
	OperationID    string  `json:"operation_id"`
	Action         string  `json:"action"`
	State          string  `json:"state"`
	AssetID        string  `json:"asset_id"`
	Revision       int64   `json:"revision"`
	AssetVersionID string  `json:"asset_version_id"`
	ErrorCode      string  `json:"error_code"`
	JobID          *string `json:"job_id"`
	EventsURL      *string `json:"events_url"`
	StatusURL      string  `json:"status_url"`
}
type AssetCatalogPreview struct {
	PreviewID        string          `json:"preview_id"`
	ExpiresAt        time.Time       `json:"expires_at"`
	RequestHash      string          `json:"request_hash"`
	Action           string          `json:"action"`
	AssetID          string          `json:"asset_id"`
	ExpectedRevision int64           `json:"expected_revision"`
	Summary          json.RawMessage `json:"summary"`
	InitialGrants    []FarmGrant     `json:"initial_grants"`
}
type AssetCatalogReceipt struct {
	SchemaVersion     string `json:"schema_version"`
	Producer          string `json:"producer"`
	TenantID          string `json:"tenant_id"`
	WorkspaceID       string `json:"workspace_id"`
	OperationID       string `json:"operation_id"`
	RequestHash       string `json:"request_hash"`
	GrantPlanHash     string `json:"grant_plan_hash"`
	AssetID           string `json:"asset_id"`
	Revision          int64  `json:"revision"`
	AssetVersionID    string `json:"asset_version_id"`
	AggregateRevision int64  `json:"aggregate_revision"`
	EventID           string `json:"event_id"`
	EventType         string `json:"event_type"`
	State             string `json:"state"`
	PayloadHash       string `json:"payload_hash"`
}

func assetCatalogView(op model.AgricultureAssetOperation) AssetCatalogOperationView {
	return AssetCatalogOperationView{OperationID: op.ID, Action: op.Action, State: op.State, AssetID: op.AssetID, Revision: op.ResultRevision, AssetVersionID: op.AssetVersionID, ErrorCode: op.ErrorCode, StatusURL: "/api/agri/v1/asset-operations/" + op.ID + "?workspace_id=" + op.WorkspaceID}
}
func validateAssetCatalogPayload(p *AssetCatalogPayload) (string, error) {
	if p.ExpectedRevision == nil {
		return "", ErrPreconditionRequired
	}
	if p.Action == "create" {
		if p.AssetID != "" || *p.ExpectedRevision != 0 || len(p.InitialGrants) < 1 || len(p.InitialGrants) > 20 {
			return "", ErrInvalid
		}
	} else if p.Action == "version" {
		if !assetCatalogID.MatchString(p.AssetID) || *p.ExpectedRevision < 1 || *p.ExpectedRevision > maxOwnershipRevision || len(p.InitialGrants) > 0 {
			return "", ErrInvalid
		}
	} else {
		return "", ErrInvalid
	}
	metadata, e := parcelDecodeMap(p.Metadata)
	if e != nil || len(metadata) != 8 {
		return "", ErrInvalid
	}
	for _, key := range []string{"name", "kind", "data_class", "farm_id", "source", "rights_declaration", "sensitivity", "retention"} {
		if _, ok := metadata[key]; !ok {
			return "", ErrInvalid
		}
	}
	farm := ""
	if metadata["farm_id"] != nil {
		var ok bool
		farm, ok = metadata["farm_id"].(string)
		if !ok || !ownershipID.MatchString(farm) {
			return "", ErrInvalid
		}
	}
	seen := map[string]bool{}
	for i := range p.InitialGrants {
		g := &p.InitialGrants[i]
		if !identityActorID.MatchString(g.SubjectUserID) || seen[g.SubjectUserID] || len(g.Capabilities) < 1 || len(g.Capabilities) > 3 {
			return "", ErrInvalid
		}
		seen[g.SubjectUserID] = true
		caps, e := canonicalBindingCapabilities(g.Capabilities)
		if e != nil {
			return "", e
		}
		for _, cap := range caps {
			if cap != "agri.asset.read" && cap != "agri.asset.create" && cap != "agri.asset_download.read" {
				return "", ErrInvalid
			}
		}
		g.Capabilities = caps
	}
	sort.Slice(p.InitialGrants, func(i, j int) bool { return p.InitialGrants[i].SubjectUserID < p.InitialGrants[j].SubjectUserID })
	return farm, nil
}
func assetCatalogPayload(op model.AgricultureAssetOperation) (AssetCatalogPayload, error) {
	var p AssetCatalogPayload
	if strictFarmJSON([]byte(op.Payload), &p, 1<<20) != nil {
		return p, ErrInvalid
	}
	_, e := validateAssetCatalogPayload(&p)
	return p, e
}
func assetCatalogBody(op model.AgricultureAssetOperation, receipt bool) ([]byte, error) {
	body := map[string]any{"operation_id": op.ID, "request_hash": op.RequestHash, "grant_plan_hash": op.GrantPlanHash}
	if !receipt {
		p, e := assetCatalogPayload(op)
		if e != nil {
			return nil, e
		}
		var id any
		if p.AssetID != "" {
			id = p.AssetID
		}
		body["asset_id"] = id
		body["expected_revision"] = *p.ExpectedRevision
		body["metadata"] = p.Metadata
	}
	raw, _, e := farmCanonical(body)
	return raw, e
}

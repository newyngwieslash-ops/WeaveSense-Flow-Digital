package agriculture

import (
	"encoding/json"
	"regexp"
	"time"
	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

const parcelReceiptType = "weavesense-agri-parcel-operation+jws"
const parcelCursorType = "weavesense-agri-parcel-cursor+jws"
const parcelAuthorityPath = "/internal/agri/v1/parcels/authority/check"
const parcelReceiptPath = "/internal/agri/v1/parcels/commands/receipt"

var parcelIDPattern = regexp.MustCompile(`^parcel_[0-9a-f]{32}$`)

type ParcelPayload struct {
	Action               string          `json:"action"`
	FarmID               string          `json:"farm_id,omitempty"`
	ParcelID             string          `json:"parcel_id,omitempty"`
	ExpectedRevision     *int64          `json:"expected_revision"`
	ExpectedFarmRevision int64           `json:"expected_farm_revision,omitempty"`
	Parcel               json.RawMessage `json:"parcel,omitempty"`
	Geometry             json.RawMessage `json:"geometry,omitempty"`
	SourceCRS            string          `json:"source_crs,omitempty"`
	CoordinateOrder      string          `json:"coordinate_order,omitempty"`
	Reason               string          `json:"reason"`
}
type ParcelPlatform struct {
	Service    Service
	Client     *GISClient
	Dispatcher flow.CommandDispatcher
}

func NewParcelPlatform(s Service, g *GISClient) *ParcelPlatform {
	p := &ParcelPlatform{Service: s, Client: g}
	p.Dispatcher = flow.CommandDispatcher{DB: s.DB, Now: s.Now, Adapters: map[string]flow.CommandAdapter{"agri.parcel.create": p, "agri.parcel.revise": p}}
	return p
}

type ParcelOperationView struct {
	OperationID      string  `json:"operation_id"`
	Action           string  `json:"action"`
	State            string  `json:"state"`
	FarmID           string  `json:"farm_id"`
	ParcelID         string  `json:"parcel_id"`
	GeometryRevision int64   `json:"geometry_revision"`
	ErrorCode        string  `json:"error_code"`
	JobID            *string `json:"job_id"`
	EventsURL        *string `json:"events_url"`
	StatusURL        string  `json:"status_url"`
}

func parcelView(op model.AgricultureParcelOperation) ParcelOperationView {
	return ParcelOperationView{OperationID: op.ID, Action: op.Action, State: op.State, FarmID: op.FarmID, ParcelID: op.ParcelID, GeometryRevision: op.ResultRevision, ErrorCode: op.ErrorCode, StatusURL: "/api/agri/v1/parcel-operations/" + op.ID + "?workspace_id=" + op.WorkspaceID}
}

type ParcelPreview struct {
	PreviewID        string          `json:"preview_id"`
	ExpiresAt        time.Time       `json:"expires_at"`
	RequestHash      string          `json:"request_hash"`
	Action           string          `json:"action"`
	FarmID           string          `json:"farm_id"`
	ParcelID         string          `json:"parcel_id"`
	ExpectedRevision int64           `json:"expected_revision"`
	Summary          json.RawMessage `json:"summary"`
}
type ParcelReceipt struct {
	SchemaVersion            string `json:"schema_version"`
	Producer                 string `json:"producer"`
	OperationID              string `json:"operation_id"`
	TenantID                 string `json:"tenant_id"`
	WorkspaceID              string `json:"workspace_id"`
	RequestHash              string `json:"request_hash"`
	FarmID                   string `json:"farm_id"`
	ParcelID                 string `json:"parcel_id"`
	GeometryRevision         int64  `json:"geometry_revision"`
	PreviousGeometryRevision int64  `json:"previous_geometry_revision"`
	PreviousGeometryHash     string `json:"previous_geometry_hash"`
	GeometryHash             string `json:"geometry_hash"`
	AggregateRevision        int64  `json:"aggregate_revision"`
	EventID                  string `json:"event_id"`
	EventType                string `json:"event_type"`
	PayloadHash              string `json:"payload_hash"`
	State                    string `json:"state"`
}

func validateParcelPayload(v ParcelPayload) error {
	if v.ExpectedRevision == nil {
		return ErrPreconditionRequired
	}
	if len(v.Reason) < 1 || len(v.Reason) > 500 {
		return ErrInvalid
	}
	switch v.Action {
	case "create":
		if !ownershipID.MatchString(v.FarmID) || v.ParcelID != "" || *v.ExpectedRevision != 0 || v.ExpectedFarmRevision < 1 || v.ExpectedFarmRevision > maximumAuthorizationVersion || len(v.Geometry) != 0 || v.SourceCRS != "" || v.CoordinateOrder != "" {
			return ErrInvalid
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(v.Parcel, &fields) != nil || len(fields) != 5 {
			return ErrInvalid
		}
		for _, k := range []string{"external_code", "name", "geometry", "source_crs", "coordinate_order"} {
			if len(fields[k]) == 0 || string(fields[k]) == "null" {
				return ErrInvalid
			}
		}
		// Parcel input has five fields; unknown data_class/identity cannot be set.
	case "revise":
		if !parcelIDPattern.MatchString(v.ParcelID) || v.FarmID != "" || *v.ExpectedRevision < 1 || *v.ExpectedRevision > maximumAuthorizationVersion || v.ExpectedFarmRevision != 0 || len(v.Parcel) > 0 || len(v.Geometry) == 0 || (v.SourceCRS != "EPSG:4326" && v.SourceCRS != "OGC:CRS84") || v.CoordinateOrder != "longitude_latitude" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
func parcelPayload(op model.AgricultureParcelOperation) (ParcelPayload, error) {
	var value ParcelPayload
	if strictFarmJSON([]byte(op.Payload), &value, 1<<20) != nil {
		return value, ErrInvalid
	}
	return value, validateParcelPayload(value)
}
func parcelResource(v ParcelPayload) (string, ResourceRef) {
	if v.Action == "create" {
		return "agri.parcel.create", ResourceRef{"farm", v.FarmID}
	}
	return "agri.parcel.update", ResourceRef{"parcel", v.ParcelID}
}
func parcelBody(op model.AgricultureParcelOperation, preview bool) ([]byte, error) {
	v, e := parcelPayload(op)
	if e != nil {
		return nil, e
	}
	body := map[string]any{"expected_revision": *v.ExpectedRevision, "reason": v.Reason}
	if v.Action == "create" {
		body["expected_farm_revision"] = v.ExpectedFarmRevision
		body["parcel"] = v.Parcel
	} else {
		body["geometry"] = v.Geometry
		body["source_crs"] = v.SourceCRS
		body["coordinate_order"] = v.CoordinateOrder
	}
	if !preview {
		body["operation_id"] = op.ID
		body["request_hash"] = op.RequestHash
	}
	raw, _, e := farmCanonical(body)
	return raw, e
}
func parcelPath(op model.AgricultureParcelOperation, preview bool) string {
	if op.Action == "create" {
		path := "/internal/agri/v1/farms/" + op.FarmID + "/parcels"
		if preview {
			path += "/preview-create"
		}
		return path
	}
	path := "/internal/agri/v1/parcels/" + op.ParcelID
	if preview {
		return path + "/preview-revise"
	}
	return path + "/revisions"
}

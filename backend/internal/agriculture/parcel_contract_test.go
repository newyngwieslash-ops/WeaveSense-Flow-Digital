package agriculture

import (
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"testing"
	"yingce/backend/internal/model"
)

func parcelContractInput() ParcelPayload {
	return ParcelPayload{Action: "create", FarmID: "farm_contract", ExpectedRevision: revisionPointer(0), ExpectedFarmRevision: 1, Parcel: json.RawMessage(`{"external_code":"parcel-a","name":"Parcel A","geometry":{"type":"Polygon","coordinates":[[[110,20],[110.001,20],[110.001,20.001],[110,20]]]},"source_crs":"EPSG:4326","coordinate_order":"longitude_latitude"}`), Reason: "Initial boundary"}
}
func TestParcelPayloadContract(t *testing.T) {
	good := parcelContractInput()
	if e := validateParcelPayload(good); e != nil {
		t.Fatal(e)
	}
	cases := []struct {
		name   string
		change func(*ParcelPayload)
		want   error
	}{
		{"missing_revision", func(p *ParcelPayload) { p.ExpectedRevision = nil }, ErrPreconditionRequired},
		{"create_nonzero_revision", func(p *ParcelPayload) { p.ExpectedRevision = revisionPointer(1) }, ErrInvalid},
		{"missing_parent_revision", func(p *ParcelPayload) { p.ExpectedFarmRevision = 0 }, ErrInvalid},
		{"client_parcel_id", func(p *ParcelPayload) { p.ParcelID = "parcel_" + strings.Repeat("a", 32) }, ErrInvalid},
		{"client_data_class", func(p *ParcelPayload) {
			p.Parcel = json.RawMessage(strings.TrimSuffix(string(p.Parcel), "}") + `,"data_class":"operational"}`)
		}, ErrInvalid},
		{"missing_reason", func(p *ParcelPayload) { p.Reason = "" }, ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := good
			tc.change(&p)
			if e := validateParcelPayload(p); !errors.Is(e, tc.want) {
				t.Fatalf("expected fixed validation error: %v", e)
			}
		})
	}
	raw, hash, e := farmCanonical(good)
	if e != nil {
		t.Fatal(e)
	}
	op := model.AgricultureParcelOperation{ID: "pop_contract", Action: "create", FarmID: good.FarmID, Payload: string(raw), RequestHash: hash}
	round, e := parcelPayload(op)
	if e != nil || *round.ExpectedRevision != 0 {
		t.Fatal("canonical typed intent roundtrip failed")
	}
	preview, e := parcelBody(op, true)
	if e != nil {
		t.Fatal(e)
	}
	write, e := parcelBody(op, false)
	if e != nil {
		t.Fatal(e)
	}
	var a, b map[string]any
	json.Unmarshal(preview, &a)
	json.Unmarshal(write, &b)
	if len(a) != 4 || len(b) != 6 || b["operation_id"] != op.ID || b["request_hash"] != hash || a["expected_revision"] != float64(0) {
		t.Fatal("preview/write binding lost or gained fields")
	}
	if parcelPath(op, true) != "/internal/agri/v1/farms/farm_contract/parcels/preview-create" {
		t.Fatal("create preview path changed")
	}
}

func TestParcelNullableHistoryContract(t *testing.T) {
	value, e := parcelDecodeMap([]byte(`{"valid_to":null,"geometry_revision":1,"affected_dependencies":{"availability":"not_yet_integrated","count":null}}`))
	if e != nil || value["valid_to"] != nil {
		t.Fatal("valid nullable historical fields rejected")
	}
	if _, e = parcelDecodeMap([]byte(`{"valid_to":null,"valid_to":"forged"}`)); e == nil {
		t.Fatal("duplicate historical field accepted")
	}
}

func TestParcelOperationHTTPContract(t *testing.T) {
	for _, tc := range []struct {
		method, state, action string
		want                  int
	}{
		{"GET", "pending", "create", 200}, {"GET", "reconciliation_required", "create", 200}, {"GET", "completed", "create", 200},
		{"POST", "pending", "create", 202}, {"POST", "reconciliation_required", "revise", 202}, {"POST", "completed", "create", 201}, {"POST", "completed", "revise", 200},
	} {
		t.Run(tc.method+"_"+tc.state+"_"+tc.action, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(tc.method, "/api/agri/v1/parcel-operations/pop_test", nil)
			op := parcelView(model.AgricultureParcelOperation{ID: "pop_test", WorkspaceID: "workspace_test", State: tc.state, Action: tc.action})
			parcelResponse(c, op, nil)
			if w.Code != tc.want {
				t.Fatalf("status %d expected %d", w.Code, tc.want)
			}
			var out struct {
				Data map[string]any `json:"data"`
			}
			if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Data["state"] != tc.state || out.Data["job_id"] != nil || out.Data["events_url"] != nil || out.Data["status_url"] != "/api/agri/v1/parcel-operations/pop_test?workspace_id=workspace_test" {
				t.Fatal("operation envelope/pending state changed")
			}
		})
	}
}

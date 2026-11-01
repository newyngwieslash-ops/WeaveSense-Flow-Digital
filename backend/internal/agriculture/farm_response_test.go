package agriculture

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
	"yingce/backend/internal/model"
)

func TestFarmResponsePersistentOperationStatusContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, row := range []struct {
		state, action string
		status        int
	}{{"pending", "create", 202}, {"reconciliation_required", "create", 202}, {"completed", "create", 201}, {"completed", "update", 200}} {
		t.Run(row.state+"_"+row.action, func(t *testing.T) {
			router := gin.New()
			router.POST("/farms", func(c *gin.Context) {
				farmResponse(c, farmOperationView(model.AgricultureFarmOperation{ID: "fop_contract", WorkspaceID: "workspace_a", Action: row.action, State: row.state}), nil)
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest("POST", "/farms", nil))
			if response.Code != row.status {
				t.Fatalf("status=%d want=%d", response.Code, row.status)
			}
			var envelope struct {
				Data map[string]any `json:"data"`
				Meta map[string]any `json:"meta"`
			}
			if json.Unmarshal(response.Body.Bytes(), &envelope) != nil {
				t.Fatal("invalid canonical envelope")
			}
			data := envelope.Data
			if data["state"] != row.state || data["operation_id"] != "fop_contract" || data["action"] != row.action || envelope.Meta["schema_version"] != "1.0" {
				t.Fatal("operation meaning changed")
			}
			if data["job_id"] != nil || data["events_url"] != nil || data["status_url"] != "/api/agri/v1/farm-operations/fop_contract?workspace_id=workspace_a" {
				t.Fatal("false job/event reference or incorrect recovery path")
			}
			// Exact FarmAPI.submit contract: 202 permits non-completed persisted
			// state, whereas synchronous 200/201 requires completed.
			if response.Code == 202 {
				if data["state"] == "completed" {
					t.Fatal("async falsely completed")
				}
			} else if data["state"] != "completed" {
				t.Fatal("sync response not completed")
			}
		})
	}
}

func TestFarmResponseStatusLookupAlwaysOK(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, state := range []string{"pending", "reconciliation_required", "completed"} {
		t.Run(state, func(t *testing.T) {
			router := gin.New()
			router.GET("/farm-operations/fop_contract", func(c *gin.Context) {
				farmResponse(c, farmOperationView(model.AgricultureFarmOperation{ID: "fop_contract", WorkspaceID: "workspace_a", Action: "create", State: state}), nil)
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest("GET", "/farm-operations/fop_contract", nil))
			if response.Code != 200 {
				t.Fatalf("status lookup returned %d", response.Code)
			}
			var envelope struct {
				Data FarmOperationView `json:"data"`
			}
			if json.Unmarshal(response.Body.Bytes(), &envelope) != nil || envelope.Data.State != state || envelope.Data.OperationID != "fop_contract" {
				t.Fatal("lookup lost persistent state")
			}
		})
	}
}

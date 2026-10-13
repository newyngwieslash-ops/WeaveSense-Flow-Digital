package flow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"yingce/backend/internal/model"
)

// The engine-facing summarize endpoint never blocks on the model: it enqueues
// a model task and returns the operation (202); the async worker completes it
// and a re-submission with the same key returns the validated result.
func TestSummarizeIsAsyncModelTask(t *testing.T) {
	db := testDB(t)
	db.Create(&model.User{ID: "u_alice", Username: "u_alice"})
	sc, err := ResolveScope(db, "u_alice", "")
	if err != nil {
		t.Fatal(err)
	}
	b := Billing{DB: db}
	fund(t, b, "u_alice", 5_000_000)
	gw := fakeGW{text: `{"body":"LLM brief","claims":[{"text":"Acme raised prices","kind":"source_statement","citations":[{"evidence_id":"ev1","revision":1}]}]}`, cost: 1000}
	cfg := Config{SigningKey: []byte("0123456789abcdef0123456789abcdef"), Issuer: "flow-platform"}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterP5Routes(r.Group("/api"), db, cfg, func(*gin.Context) (*model.User, error) { return nil, nil }, gw)
	tok, _ := Sign(cfg.SigningKey, ExecutionContext{Issuer: "loom-engine", Audience: "flow-platform", TenantID: sc.TenantID, WorkspaceID: sc.WorkspaceID,
		Actor: Actor{"service_principal", "loom-engine"}, PolicyVersion: "policy_v1"}, time.Now(), time.Minute)
	body := `{"grant_actor":"user:u_alice","workspace_id":"` + sc.WorkspaceID + `","key":"b1:w1","topic":"Acme",
		"scope":{"source_ids":["s1"]},"evidence":[{"evidence_id":"ev1","document_id":"d1","source_id":"s1","revision":1,"quote":"Acme raised prices"}]}`
	call := func() (int, map[string]any) {
		req := httptest.NewRequest("POST", "/api/internal/flow/summarize", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, out := call()
	if code != http.StatusAccepted {
		t.Fatalf("first call should enqueue: %d %v", code, out)
	}
	taskID := out["data"].(map[string]any)["task_id"].(string)
	if again, out2 := call(); again != http.StatusAccepted || out2["data"].(map[string]any)["task_id"] != taskID {
		t.Fatalf("re-submit not idempotent: %d %v", again, out2)
	}
	// Async pipeline (worker) completes the task.
	if n, err := (ModelTasks{DB: db, GW: gw}).RunDue(context.Background()); err != nil || n != 1 {
		t.Fatalf("worker ran %d: %v", n, err)
	}
	code, out = call()
	data, _ := out["data"].(map[string]any)
	if code != http.StatusOK || data["body"] != "LLM brief" || len(data["claims"].([]any)) != 1 || data["task_id"] != taskID {
		t.Fatalf("completion: %d %v", code, out)
	}
	var n int64
	db.Model(&model.FlowModelTask{}).Count(&n)
	if n != 1 {
		t.Fatalf("%d tasks for one key", n)
	}
}

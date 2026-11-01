package handler

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"yingce/backend/internal/auth"
	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
	"yingce/backend/internal/service"
)

// Real loopback HTTP + actual persisted sessions and stream content. No event
// publisher, auth provider or successful response is mocked.
func TestConnectedLegacyStreamsRevalidateAuthority(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, kind := range []string{"text", "agent"} {
		for _, change := range []string{"logout", "expired", "disabled", "ownership", "other_cookie", "success"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
				if err != nil {
					t.Fatal(err)
				}
				sqlDB, _ := db.DB()
				sqlDB.SetMaxOpenConns(1)
				t.Cleanup(func() { _ = sqlDB.Close() })
				if err := db.AutoMigrate(&model.User{}, &model.AuthSession{}, &model.Task{}, &model.TaskTextDelta{}, &model.CloudAgentExecution{}, &model.CloudAgentEventRecord{}, &model.CloudAgentMessageRecord{}, &model.BillingOrder{}, &model.SystemSetting{}); err != nil {
					t.Fatal(err)
				}
				seedStreamIdentity(t, db, "user")
				if err := db.Create(&model.AuthSession{ID: "second-session", UserID: "user", TokenHash: auth.HashToken("second-secret"), ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
					t.Fatal(err)
				}
				svc := service.New(repository.New(db), t.TempDir())
				t.Cleanup(func() { _ = svc.Close() })
				id := "task"
				input := "{}"
				if kind == "agent" {
					h := sha256.Sum256([]byte("user\x00stream-fixture"))
					id = "ag" + hex.EncodeToString(h[:16])
					input = `{"cloudAgent":{"version":1,"request":{"idempotencyKey":"stream-fixture"}}}`
				}
				if err := db.Create(&model.Task{ID: id, UserID: "user", Operation: "cloud_agent", Status: model.TaskStatusRunning, InputJSON: input}).Error; err != nil {
					t.Fatal(err)
				}
				state := func(secret string) string {
					seq := 1
					if secret != "initial-public-marker" {
						seq = 2
					}
					b, _ := json.Marshal(map[string]any{"eventSeqBase": seq - 1, "events": []any{map[string]any{"seq": seq, "type": "fixture", "payload": map[string]string{"text": secret}}}})
					return string(b)
				}
				if kind == "agent" {
					if err := db.Create(&model.CloudAgentExecution{ID: id, UserID: "user", Status: "running", Revision: 1, StateJSON: state("initial-public-marker")}).Error; err != nil {
						t.Fatal(err)
					}
				}
				router := gin.New()
				RegisterTaskRoutes(router.Group("/api"), svc)
				RegisterAgentRoutes(router.Group("/api"), svc)
				server := httptest.NewServer(router)
				t.Cleanup(server.Close)
				path := "/api/tasks/" + id + "/text-events"
				marker := "event: progress"
				if kind == "agent" {
					path = "/api/agent/runs/" + id + "/events?after=1"
					marker = "event: run_snapshot"
				}
				ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
				defer cancel()
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
				req.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: "stream-session.stream-secret"})
				resp, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != 200 {
					t.Fatalf("initial stream status %d", resp.StatusCode)
				}
				if !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
					t.Fatal("stream cacheable")
				}
				reader := bufio.NewReader(resp.Body)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						t.Fatalf("initial frame missing: %v", err)
					}
					if strings.Contains(line, marker) {
						break
					}
				}
				// Finish that already authorized frame before revoking. Its bytes are
				// intentionally not claimed recoverable after the fact.
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						t.Fatal(err)
					}
					if line == "\n" {
						break
					}
				}
				switch change {
				case "logout":
					err = svc.Logout("stream-session.stream-secret")
				case "expired":
					err = db.Model(&model.AuthSession{}).Where("id = ?", "stream-session").Update("expires_at", time.Now().Add(-time.Second)).Error
				case "disabled":
					err = db.Model(&model.User{}).Where("id = ?", "user").Update("status", "disabled").Error
				case "ownership":
					err = db.Model(&model.Task{}).Where("id = ?", id).Update("user_id", "other").Error
				case "other_cookie":
					err = svc.Logout("second-session.second-secret")
				}
				if err != nil {
					t.Fatal(err)
				}
				const sensitive = "new-sensitive-after-transition"
				if kind == "text" {
					if err = db.Create(&model.TaskTextDelta{ID: "late", UserID: "user", TaskID: id, Sequence: 1, Content: sensitive, ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
						t.Fatal(err)
					}
					err = db.Model(&model.Task{}).Where("id = ?", id).Update("status", model.TaskStatusSucceeded).Error
				} else {
					// Durable journal replay does not require a live model runtime.
					// Start after sequence 1 and append the real sequence 2 row.
					event, _ := json.Marshal(map[string]any{"eventId": id + ":2", "runId": id, "seq": 2, "type": "fixture", "payload": map[string]string{"text": sensitive}})
					if err = db.Create(&model.CloudAgentEventRecord{RunID: id, UserID: "user", Sequence: 2, EventJSON: string(event)}).Error; err != nil {
						t.Fatal(err)
					}
					err = db.Model(&model.CloudAgentExecution{}).Where("id = ?", id).Updates(map[string]any{"revision": 2, "status": "completed", "state_json": state(sensitive)}).Error
				}
				if err != nil {
					t.Fatal(err)
				}
				remaining, err := io.ReadAll(reader)
				if err != nil {
					t.Fatalf("stream failed to close: %v", err)
				}
				allowed := change == "success" || change == "other_cookie"
				if strings.Contains(string(remaining), sensitive) != allowed {
					t.Fatalf("sensitive delivery mismatch allowed=%v error=%v snapshot=%v terminal=%v bytes=%d", allowed, strings.Contains(string(remaining), "event: error"), strings.Contains(string(remaining), "run_snapshot"), strings.Contains(string(remaining), "terminal"), len(remaining))
				}
				if allowed && !strings.Contains(string(remaining), "terminal") {
					t.Fatal("successful stream missing terminal")
				}
				if !allowed {
					reconnect, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
					reconnect.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: "stream-session.stream-secret"})
					reconnect.Header.Set("Last-Event-ID", "1")
					r, err := server.Client().Do(reconnect)
					if err != nil {
						t.Fatal(err)
					}
					defer r.Body.Close()
					if r.StatusCode == http.StatusOK {
						t.Fatal("revoked reconnect admitted")
					}
				}
			})
		}
	}
}

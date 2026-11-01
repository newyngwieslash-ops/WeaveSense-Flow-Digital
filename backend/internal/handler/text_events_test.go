package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/auth"
	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
	"yingce/backend/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestTaskTextEventCursorPrefersQueryAndSupportsLastEventID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest("GET", "/api/tasks/task-1/text-events?after=7", nil)
	request.Header.Set("Last-Event-ID", "3")
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	after, err := taskTextEventCursor(context)
	if err != nil || after != 7 {
		t.Fatalf("cursor = %d, err = %v", after, err)
	}

	request = httptest.NewRequest("GET", "/api/tasks/task-1/text-events", nil)
	request.Header.Set("Last-Event-ID", "3")
	context, _ = gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	after, err = taskTextEventCursor(context)
	if err != nil || after != 3 {
		t.Fatalf("Last-Event-ID cursor = %d, err = %v", after, err)
	}
}

func TestTaskTextEventCursorRejectsInvalidValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, query := range []string{"after=abc", "after=-1"} {
		t.Run(query, func(t *testing.T) {
			request := httptest.NewRequest("GET", "/api/tasks/task-1/text-events?"+query, nil)
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			context.Request = request
			if after, err := taskTextEventCursor(context); err == nil || after != 0 {
				t.Fatalf("cursor = %d, err = %v; want a rejected cursor", after, err)
			}
		})
	}
}

func TestStreamTaskTextEventsCachedReplayReachesTerminalWithoutDuplicateDelta(t *testing.T) {
	t.Setenv("REDIS_URL", "")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.AuthSession{}, &model.Task{}, &model.TaskTextDelta{}); err != nil {
		t.Fatal(err)
	}
	seedStreamIdentity(t, db, "user")
	if err := db.Create(&model.Task{ID: "task", UserID: "user", Status: model.TaskStatusRunning}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.TaskTextDelta{ID: "delta", UserID: "user", TaskID: "task", Sequence: 8, Content: "text", ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	svc := service.New(repository.New(db), t.TempDir())
	t.Cleanup(func() { _ = svc.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	initial, err := svc.CachedTaskTextReplay(ctx, "user", "task", 7)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", "task").Updates(map[string]any{"status": model.TaskStatusSucceeded, "progress": 100}).Error; err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest("GET", "/api/tasks/task/text-events", nil).WithContext(ctx)
	c.Request.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: "stream-session.stream-secret"})
	streamTaskTextEvents(c, svc, "user", "task", 7, initial)
	body := response.Body.String()
	if strings.Count(body, "id: 8\nevent: delta") != 1 || !strings.Contains(body, "event: terminal") || !strings.Contains(body, `"progress":100`) {
		t.Fatalf("unexpected cached SSE stream: %q", body)
	}
}

func TestStreamTaskTextEventsWritesSequenceAndTerminalEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest("GET", "/api/tasks/task-1/text-events", nil)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.AuthSession{}, &model.Task{}); err != nil {
		t.Fatal(err)
	}
	seedStreamIdentity(t, db, "user-1")
	if err := db.Create(&model.Task{ID: "task-1", UserID: "user-1"}).Error; err != nil {
		t.Fatal(err)
	}
	svc := service.New(repository.New(db), t.TempDir())
	t.Cleanup(func() { _ = svc.Close() })
	context.Request.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: "stream-session.stream-secret"})
	replay := &service.TextReplayResult{
		Deltas:    []model.TaskTextDelta{{Sequence: 8, Content: "增量"}},
		Complete:  true,
		FinalText: "增量",
		Status:    model.TaskStatusSucceeded,
		Stage:     "已完成",
		Progress:  100,
	}
	streamTaskTextEvents(context, svc, "user-1", "task-1", 7, replay)
	if response.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" {
		t.Fatalf("content type = %q", response.Header().Get("Content-Type"))
	}
	body := response.Body.String()
	if !strings.Contains(body, "event: progress") || !strings.Contains(body, `"progress":100`) || !strings.Contains(body, "id: 8\nevent: delta") || !strings.Contains(body, "event: terminal") {
		t.Fatalf("unexpected SSE body: %q", body)
	}
}

func seedStreamIdentity(t *testing.T, db *gorm.DB, id string) {
	t.Helper()
	for _, row := range []any{
		&model.User{ID: id, Username: id, Status: model.UserStatusActive},
		&model.AuthSession{ID: "stream-session", UserID: id, TokenHash: auth.HashToken("stream-secret"), ExpiresAt: time.Now().Add(time.Hour)},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
}

type revokingStreamWriter struct {
	*httptest.ResponseRecorder
	onDelta     func()
	onConnected func()
}

func (w *revokingStreamWriter) Flush() {
	w.ResponseRecorder.Flush()
	if w.onConnected != nil && strings.Contains(w.Body.String(), ": connected") {
		fn := w.onConnected
		w.onConnected = nil
		fn()
	}
	if w.onDelta != nil && strings.Contains(w.Body.String(), "event: delta") {
		fn := w.onDelta
		w.onDelta = nil
		fn()
	}
}

func TestTaskStreamRevocationDuringConnectedFlush(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.AuthSession{}, &model.Task{}); err != nil {
		t.Fatal(err)
	}
	seedStreamIdentity(t, db, "user")
	if err := db.Create(&model.Task{ID: "task", UserID: "user"}).Error; err != nil {
		t.Fatal(err)
	}
	svc := service.New(repository.New(db), t.TempDir())
	t.Cleanup(func() { _ = svc.Close() })
	w := &revokingStreamWriter{ResponseRecorder: httptest.NewRecorder(), onConnected: func() {
		if err := svc.Logout("stream-session.stream-secret"); err != nil {
			t.Fatal(err)
		}
	}}
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Request.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: "stream-session.stream-secret"})
	streamTaskTextEvents(c, svc, "user", "task", 0, &service.TextReplayResult{Complete: true, Stage: "private-stage", Progress: 78, Status: model.TaskStatusSucceeded})
	body := w.Body.String()
	if !strings.Contains(body, ": connected") || strings.Contains(body, "event:") || strings.Contains(body, "private-stage") || strings.Contains(body, "status") {
		t.Fatal("initial flush bypassed revocation before progress")
	}
}

func TestTaskStreamRevocationBetweenBufferedEvents(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.AuthSession{}, &model.Task{}); err != nil {
		t.Fatal(err)
	}
	seedStreamIdentity(t, db, "user")
	if err := db.Create(&model.Task{ID: "task", UserID: "user"}).Error; err != nil {
		t.Fatal(err)
	}
	svc := service.New(repository.New(db), t.TempDir())
	t.Cleanup(func() { _ = svc.Close() })
	w := &revokingStreamWriter{ResponseRecorder: httptest.NewRecorder(), onDelta: func() {
		if err := svc.Logout("stream-session.stream-secret"); err != nil {
			t.Fatal(err)
		}
	}}
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Request.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: "stream-session.stream-secret"})
	streamTaskTextEvents(c, svc, "user", "task", 0, &service.TextReplayResult{Complete: true, Deltas: []model.TaskTextDelta{{Sequence: 1, Content: "already-admitted"}, {Sequence: 2, Content: "must-not-send"}}})
	if !strings.Contains(w.Body.String(), "already-admitted") || strings.Contains(w.Body.String(), "must-not-send") || strings.Contains(w.Body.String(), "event: terminal") {
		t.Fatal("buffered events bypassed current session check")
	}
}

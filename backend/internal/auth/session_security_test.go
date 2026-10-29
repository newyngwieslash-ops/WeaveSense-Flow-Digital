package auth

import (
	"errors"
	"testing"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
)

// Uses the real GORM/SQLite repository, not a permissive session-store mock.
// PostgreSQL tenant/RLS acceptance is a separate integration suite.
func newSessionSecurityService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	svc, db := newPasswordResetTestService(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Create(&model.User{ID: "session-owner", Username: "session-owner", Role: model.UserRoleUser, Status: model.UserStatusActive}).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"victim", "other-browser"} {
		if err := db.Create(&model.AuthSession{ID: id, UserID: "session-owner", TokenHash: HashToken(id + "-secret"), ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return svc, db
}

func requireSessionPresent(t *testing.T, db *gorm.DB, id string) {
	t.Helper()
	var session model.AuthSession
	if err := db.First(&session, "id = ?", id).Error; err != nil {
		t.Fatalf("session %s unexpectedly removed: %v", id, err)
	}
}

func TestForgedCookieCannotRevokeValidSession(t *testing.T) {
	svc, db := newSessionSecurityService(t)
	for _, cookie := range []string{"victim.wrong", "victim.other-browser-secret", "victim.", "victim", ".victim-secret", "", "unknown.victim-secret"} {
		t.Run(cookie, func(t *testing.T) {
			if _, err := svc.CurrentUser(cookie); err == nil {
				t.Fatal("forged cookie authenticated")
			}
			requireSessionPresent(t, db, "victim")
			if err := svc.Logout(cookie); err != nil {
				t.Fatalf("invalid logout should be an idempotent no-op: %v", err)
			}
			requireSessionPresent(t, db, "victim")
			requireSessionPresent(t, db, "other-browser")
			if user, err := svc.CurrentUser("victim.victim-secret"); err != nil || user.ID != "session-owner" {
				t.Fatalf("legitimate session no longer usable: %v", err)
			}
		})
	}
}

func TestVerifiedLogoutRevokesOnlyItsOwnSession(t *testing.T) {
	svc, db := newSessionSecurityService(t)
	for i := 0; i < 2; i++ {
		if err := svc.Logout("victim.victim-secret"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.CurrentUser("victim.victim-secret"); err == nil {
		t.Fatal("logged-out session still authenticates")
	}
	requireSessionPresent(t, db, "other-browser")
	if _, err := svc.CurrentUser("other-browser.other-browser-secret"); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredSessionDeniedAndVerifiedCookieCleansItUp(t *testing.T) {
	svc, db := newSessionSecurityService(t)
	if err := db.Model(&model.AuthSession{}).Where("id = ?", "victim").Update("expires_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CurrentUser("victim.wrong"); err == nil {
		t.Fatal("wrong token authenticated")
	}
	requireSessionPresent(t, db, "victim")
	if _, err := svc.CurrentUser("victim.victim-secret"); err == nil {
		t.Fatal("expired session authenticated")
	}
	var session model.AuthSession
	if err := db.First(&session, "id = ?", "victim").Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expired session was not removed: %v", err)
	}
	requireSessionPresent(t, db, "other-browser")
}

func TestDisabledUserDeniedButCanLogoutTheirOwnSession(t *testing.T) {
	svc, db := newSessionSecurityService(t)
	if err := db.Model(&model.User{}).Where("id = ?", "session-owner").Update("status", model.UserStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CurrentUser("victim.victim-secret"); err == nil {
		t.Fatal("disabled account authenticated")
	}
	if err := svc.Logout("victim.victim-secret"); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&model.AuthSession{}).Where("id = ?", "victim").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("disabled user's logout failed: count=%d err=%v", count, err)
	}
}

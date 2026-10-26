package edumath

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"gorm.io/gorm"

	"yingce/backend/internal/model"
)

// MaxURLTTL bounds signed artifact URLs (FR-A07: <= 5 minutes).
const MaxURLTTL = 5 * time.Minute

func (s *Service) urlSig(artifactID, userID string, exp int64) string {
	mac := hmac.New(sha256.New, s.URLKey)
	fmt.Fprintf(mac, "edu-artifact\n%s\n%s\n%d", artifactID, userID, exp)
	return hex.EncodeToString(mac.Sum(nil))
}

// SignedURL returns a short-lived path bound to the artifact, the user and the expiry.
func (s *Service) SignedURL(artifactID, userID string, ttl time.Duration) (string, time.Time) {
	if ttl <= 0 || ttl > MaxURLTTL {
		ttl = MaxURLTTL
	}
	exp := s.now().Add(ttl).Truncate(time.Second)
	q := url.Values{"u": {userID}, "exp": {strconv.FormatInt(exp.Unix(), 10)}, "sig": {s.urlSig(artifactID, userID, exp.Unix())}}
	return "/api/edu/files/" + artifactID + "?" + q.Encode(), exp
}

// VerifyURL checks signature and expiry; access is re-checked separately so a
// revocation or withdrawal blocks new fetches immediately.
func (s *Service) VerifyURL(artifactID, userID, expS, sig string) bool {
	exp, err := strconv.ParseInt(expS, 10, 64)
	if err != nil || userID == "" || len(s.URLKey) < 32 {
		return false
	}
	now := s.now().Unix()
	if exp < now || exp > now+int64(MaxURLTTL/time.Second)+1 {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(s.urlSig(artifactID, userID, exp)))
}

// canFetch re-evaluates authorization for the bound user (system context, explicit filters).
func canFetch(db *gorm.DB, art model.EduArtifact, userID string) bool {
	if art.RevokedAt != nil {
		return false
	}
	rs, err := roles(db, art.TenantID, userID)
	if err != nil {
		return false
	}
	if art.Audience == "teacher" {
		var job model.EduMathJob
		if db.First(&job, "id = ? AND tenant_id = ?", art.JobID, art.TenantID).Error != nil {
			return false
		}
		if !(rs["teacher"] || rs["reviewer"] || rs["org_admin"]) {
			return false
		}
		var n int64
		db.Model(&model.WorkspaceMembership{}).Where("workspace_id = ? AND user_id = ?", job.WorkspaceID, userID).Count(&n)
		return n > 0
	}
	if art.Audience != "student" {
		return false
	}
	if rs["teacher"] || rs["reviewer"] || rs["org_admin"] {
		// Staff may preview the student projection of their own workspace's content.
		var job model.EduMathJob
		if db.First(&job, "id = ? AND tenant_id = ?", art.JobID, art.TenantID).Error != nil {
			return false
		}
		var n int64
		db.Model(&model.WorkspaceMembership{}).Where("workspace_id = ? AND user_id = ?", job.WorkspaceID, userID).Count(&n)
		if n > 0 {
			return true
		}
	}
	// Student: the artifact must belong to a published lesson assigned to a class they are enrolled in.
	var lessons []model.EduLessonRevision
	db.Where("tenant_id = ? AND (student_artifact = ? OR student_preview = ?)", art.TenantID, art.ID, art.ID).Find(&lessons)
	for _, l := range lessons {
		var pubs []model.EduPublication
		db.Where("tenant_id = ? AND lesson_revision_id = ? AND state = ?", art.TenantID, l.ID, "published").Find(&pubs)
		for _, p := range pubs {
			var as []model.EduAssignment
			db.Where("tenant_id = ? AND publication_id = ? AND state = ?", art.TenantID, p.ID, "active").Find(&as)
			for _, a := range as {
				if _, _, err := studentCanAccessAssignment(db, art.TenantID, userID, a.ID); err == nil {
					return true
				}
			}
		}
	}
	return false
}

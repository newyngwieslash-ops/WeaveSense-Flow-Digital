package edumath

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// ---------------------------------------------------------------------------
// Outbox (DEV-057): job facts written in the state change's own transaction.

// jobEvent appends the next fact of a job. Seq is per job and gap free; the
// unique (job_id, seq) index makes a concurrent writer fail its whole
// transaction instead of producing a duplicate or a hole.
func (s *Service) jobEvent(tx *gorm.DB, job model.EduMathJob, typ string, serviceSeq int64, detail string) error {
	var last int64
	if err := tx.Model(&model.EduJobEvent{}).Where("job_id = ?", job.ID).Select("COALESCE(MAX(seq), 0)").Scan(&last).Error; err != nil {
		return err
	}
	if len(detail) > 200 {
		detail = detail[:200]
	}
	return tx.Create(&model.EduJobEvent{ID: flow.NewID("eje_"), TenantID: job.TenantID, JobID: job.ID, Seq: last + 1, Type: typ, Status: job.Status,
		Settlement: job.Settlement, ServiceSeq: serviceSeq, Detail: detail, CreatedAt: s.now()}).Error
}

// jobEvents lists a job's facts after a consumer's last seen seq (staff only).
func (h *API) jobEvents(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	j, ok := h.loadJob(c, tx, a)
	if !ok {
		return
	}
	after, err := strconv.ParseInt(c.DefaultQuery("after", "0"), 10, 64)
	if err != nil || after < 0 {
		reply(c, 400, nil, "after must be a non-negative integer")
		return
	}
	var evs []model.EduJobEvent
	if err := tx.Where("job_id = ? AND seq > ?", j.ID, after).Order("seq").Limit(200).Find(&evs).Error; err != nil {
		h.fail(c, err)
		return
	}
	reply(c, 200, gin.H{"jobId": j.ID, "events": evs}, "ok")
}

// ---------------------------------------------------------------------------
// Inbox (DEV-056): signed completion events pushed by the math service.

const (
	EventSchema       = "edu.math.event/1"
	maxEventBytes     = 4096
	eventClockSkewSec = 300
)

var (
	eventIDRe   = regexp.MustCompile(`^me_[0-9a-f]{32}$`)
	platformJob = regexp.MustCompile(`^emj_[0-9A-Za-z]{1,36}$`)
)

// MathEvent is the body of a pushed event. It is a hint, never a fact: the job
// is re-read from the service before any state changes.
type MathEvent struct {
	SchemaVersion string `json:"schema_version"`
	EventID       string `json:"event_id"`
	TenantID      string `json:"tenant_id"`
	JobID         string `json:"job_id"`
	RequestID     string `json:"request_id"`
	Seq           int64  `json:"seq"`
	Status        string `json:"status"`
	SentAt        int64  `json:"sent_at"`
}

var errEventAuth = errors.New("edumath: event signature invalid")

// VerifyEvent checks the service signature, the schema and freshness.
func (s *Service) VerifyEvent(body []byte, sig string) (MathEvent, error) {
	var ev MathEvent
	mac := hmac.New(sha256.New, s.Client.Key)
	mac.Write([]byte("event\n"))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	if len(s.Client.Key) < 32 || !hmac.Equal([]byte(want), []byte(sig)) {
		return ev, errEventAuth
	}
	if err := strictJSON(body, &ev); err != nil {
		return ev, ErrContract
	}
	if ev.SchemaVersion != EventSchema || !eventIDRe.MatchString(ev.EventID) || !serviceIDRe.MatchString(ev.JobID) || ev.JobID[:3] != "mj_" ||
		ev.TenantID == "" || len(ev.TenantID) > 40 || ev.Seq < 1 || !jobStatuses[ev.Status] ||
		(ev.RequestID != "" && !platformJob.MatchString(ev.RequestID)) {
		return ev, ErrContract
	}
	if d := s.now().Unix() - ev.SentAt; d > eventClockSkewSec || d < -eventClockSkewSec {
		return ev, errEventAuth // replay outside the window
	}
	return ev, nil
}

// HandleMathEvent records the event once and, when it is news, re-reads the job
// from the service (Sync). Results: duplicate, stale, unknown_job, applied.
func (s *Service) HandleMathEvent(ctx context.Context, ev MathEvent) (string, error) {
	result := ""
	err := flow.TenantTx(s.DB, ev.TenantID, func(tx *gorm.DB) error {
		rec := model.EduMathEvent{ID: ev.EventID, TenantID: ev.TenantID, ServiceJobID: ev.JobID, Seq: ev.Seq, Status: ev.Status, ReceivedAt: s.now()}
		ins := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rec)
		if ins.Error != nil {
			return ins.Error
		}
		if ins.RowsAffected == 0 {
			result = "duplicate"
			return nil
		}
		var job model.EduMathJob
		q := tx.Where("tenant_id = ?", ev.TenantID)
		if ev.RequestID != "" {
			q = q.Where("id = ?", ev.RequestID)
		} else {
			q = q.Where("service_job_id = ?", ev.JobID)
		}
		switch {
		case q.First(&job).Error != nil, job.ServiceJobID != "" && job.ServiceJobID != ev.JobID:
			result = "unknown_job"
		case job.ServiceJobID != "" && ev.Seq <= job.Seq:
			result = "stale"
		default:
			if _, err := s.Sync(ctx, tx, job); err != nil {
				return err // rolled back with the inbox row: the service delivers again
			}
			result = "applied"
		}
		return tx.Model(&rec).Updates(map[string]any{"job_id": job.ID, "result": result}).Error
	})
	return result, err
}

func (h *API) mathEvent(c *gin.Context) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxEventBytes+1))
	if err != nil || len(body) > maxEventBytes {
		reply(c, http.StatusRequestEntityTooLarge, nil, "event too large")
		return
	}
	ev, err := h.Svc.VerifyEvent(body, c.GetHeader("X-Math-Event-Signature"))
	if errors.Is(err, errEventAuth) {
		reply(c, 401, nil, "invalid event signature")
		return
	}
	if err != nil {
		reply(c, 400, nil, "invalid event")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	res, err := h.Svc.HandleMathEvent(ctx, ev)
	if err != nil {
		reply(c, 503, nil, "event not applied; retry")
		return
	}
	reply(c, 202, gin.H{"result": res}, "accepted")
}

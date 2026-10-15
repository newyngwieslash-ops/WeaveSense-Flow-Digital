package flow

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// §5.2 / §12 external notifications. A report.completed event fans out (inside the
// inbox transaction, so exactly once per event) into one delivery per authorised
// target; each delivery has a stable delivery_id, bounded retries and a receipt.
// A target that is revoked or permanently rejects stops; nothing is re-routed.

const DefaultNotifyEvent = "report.completed"

var (
	ErrNotifyTarget = errors.New("flow: invalid notification target")
	errPermanent    = errors.New("permanent delivery failure")
	errAmbiguous    = errors.New("delivery outcome unknown")
)

// TargetInput is the API shape for creating a target.
type TargetInput struct {
	Channel    string   `json:"channel"`
	Address    string   `json:"address"`
	SecretRef  string   `json:"secretRef"`
	EventTypes []string `json:"eventTypes"`
}

// AllowInsecureWebhooks permits http:// and private addresses (tests only).
var AllowInsecureWebhooks = false

func validateTarget(in TargetInput) (TargetInput, error) {
	in.Channel, in.Address = strings.TrimSpace(in.Channel), strings.TrimSpace(in.Address)
	switch in.Channel {
	case "email":
		a, err := mail.ParseAddress(in.Address)
		if err != nil || a.Address != in.Address || len(in.Address) > 254 {
			return in, fmt.Errorf("%w: invalid email address", ErrNotifyTarget)
		}
		in.SecretRef = ""
	case "webhook":
		u, err := url.Parse(in.Address)
		if err != nil || u.Host == "" || u.User != nil || len(in.Address) > 500 || (u.Scheme != "https" && !(AllowInsecureWebhooks && u.Scheme == "http")) {
			return in, fmt.Errorf("%w: webhook must be an https URL without credentials", ErrNotifyTarget)
		}
		if in.SecretRef != "" && !strings.HasPrefix(in.SecretRef, "env:") {
			return in, fmt.Errorf("%w: secretRef must be env:NAME", ErrNotifyTarget)
		}
	default:
		return in, fmt.Errorf("%w: channel must be email or webhook", ErrNotifyTarget)
	}
	if len(in.EventTypes) == 0 {
		in.EventTypes = []string{DefaultNotifyEvent}
	}
	for _, t := range in.EventTypes {
		if t != "report.completed" && t != "operation.failed" && t != "schedule.blocked" && t != "research.completed" {
			return in, fmt.Errorf("%w: unsupported event type %q", ErrNotifyTarget, t)
		}
	}
	return in, nil
}

func CreateNotificationTarget(db *gorm.DB, s Scope, userID string, in TargetInput, now time.Time) (model.FlowNotificationTarget, error) {
	in, err := validateTarget(in)
	if err != nil {
		return model.FlowNotificationTarget{}, err
	}
	t := model.FlowNotificationTarget{ID: NewID("ntg_"), TenantID: s.TenantID, WorkspaceID: s.WorkspaceID, Channel: in.Channel, Address: in.Address,
		SecretRef: in.SecretRef, EventTypes: strings.Join(in.EventTypes, ","), Status: "active", CreatedBy: userID, CreatedAt: now, UpdatedAt: now}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&t).Error; err != nil {
		return t, err
	}
	var out model.FlowNotificationTarget
	err = db.First(&out, "tenant_id = ? AND workspace_id = ? AND channel = ? AND address = ?", s.TenantID, s.WorkspaceID, in.Channel, in.Address).Error
	if err == nil && out.Status != "active" {
		// Re-adding a revoked/disabled target is an explicit re-authorisation.
		out.Status, out.DisabledReason, out.SecretRef, out.EventTypes, out.UpdatedAt = "active", "", in.SecretRef, strings.Join(in.EventTypes, ","), now
		err = db.Save(&out).Error
	}
	return out, err
}

func ListNotificationTargets(db *gorm.DB, s Scope) ([]model.FlowNotificationTarget, error) {
	var out []model.FlowNotificationTarget
	return out, db.Where("tenant_id = ? AND workspace_id = ?", s.TenantID, s.WorkspaceID).Order("created_at").Find(&out).Error
}

// RevokeNotificationTarget stops the target; its pending deliveries are cancelled.
func RevokeNotificationTarget(db *gorm.DB, s Scope, id string, now time.Time) error {
	return db.Transaction(func(tx *gorm.DB) error {
		r := tx.Model(&model.FlowNotificationTarget{}).Where("id = ? AND tenant_id = ? AND workspace_id = ?", id, s.TenantID, s.WorkspaceID).
			Updates(map[string]any{"status": "revoked", "disabled_reason": "revoked by user", "updated_at": now})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected == 0 {
			return ErrNotFound
		}
		return cancelTargetDeliveries(tx, id, "target revoked", now)
	})
}

func cancelTargetDeliveries(tx *gorm.DB, targetID, reason string, now time.Time) error {
	return tx.Model(&model.FlowNotificationDelivery{}).Where("target_id = ? AND status IN ?", targetID, []string{"pending", StatusRetryWait}).
		Updates(map[string]any{"status": "cancelled", "last_error": reason, "updated_at": now}).Error
}

func ListNotificationDeliveries(db *gorm.DB, s Scope, status string) ([]model.FlowNotificationDelivery, error) {
	var out []model.FlowNotificationDelivery
	q := db.Where("tenant_id = ? AND workspace_id = ?", s.TenantID, s.WorkspaceID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	return out, q.Order("created_at DESC").Limit(200).Find(&out).Error
}

// fanOutDeliveries creates one delivery per active, subscribed target (idempotent per event).
func fanOutDeliveries(tx *gorm.DB, e Event, title, refID string, now time.Time) error {
	var targets []model.FlowNotificationTarget
	if err := tx.Where("tenant_id = ? AND workspace_id = ? AND status = ?", e.TenantID, e.WorkspaceID, "active").Find(&targets).Error; err != nil {
		return err
	}
	for _, t := range targets {
		if !strings.Contains(","+t.EventTypes+",", ","+e.Type+",") {
			continue
		}
		d := model.FlowNotificationDelivery{ID: NewID("dlv_"), TenantID: e.TenantID, WorkspaceID: e.WorkspaceID, TargetID: t.ID, EventID: e.EventID,
			EventType: e.Type, Title: truncRunes(title, 300), RefID: refID, Status: "pending", MaxAttempts: 5, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&d).Error; err != nil {
			return err
		}
	}
	return nil
}

// Mailer sends one message and returns a receipt. errAmbiguous-wrapped errors mean the
// server may have accepted it (lost reply after DATA) → reconciliation_required, not resent.
type Mailer func(ctx context.Context, to, subject, body string, headers map[string]string) (receipt string, err error)

// Notifier delivers due notifications.
type Notifier struct {
	DB      *gorm.DB
	Mail    Mailer
	HTTP    *http.Client
	Now     func() time.Time
	Backoff time.Duration
	// BaseURL links reports in messages (e.g. https://flow.example.com).
	BaseURL string
}

func (n Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now().UTC()
}

// RunDue claims due deliveries (lease) and sends them. Returns the number attempted.
func (n Notifier) RunDue(ctx context.Context) (int, error) {
	now := n.now()
	var due []model.FlowNotificationDelivery
	if err := n.DB.Where("status IN ? AND next_attempt_at <= ?", []string{"pending", StatusRetryWait}, now).Order("next_attempt_at").Limit(20).Find(&due).Error; err != nil {
		return 0, err
	}
	// A crashed sender's lease: the send may have happened → reconciliation_required.
	n.DB.Model(&model.FlowNotificationDelivery{}).Where("status = ? AND lease_until < ?", "sending", now).
		Updates(map[string]any{"status": StatusReconciliation, "last_error": "sender lease expired", "updated_at": now})
	done := 0
	for _, d := range due {
		claim := n.DB.Model(&model.FlowNotificationDelivery{}).Where("id = ? AND status = ?", d.ID, d.Status).
			Updates(map[string]any{"status": "sending", "lease_until": now.Add(2 * time.Minute), "attempts": gorm.Expr("attempts + 1"), "updated_at": now})
		if claim.Error != nil || claim.RowsAffected == 0 {
			continue
		}
		d.Attempts++
		done++
		n.deliver(ctx, d)
	}
	return done, nil
}

func (n Notifier) deliver(ctx context.Context, d model.FlowNotificationDelivery) {
	now := n.now()
	var t model.FlowNotificationTarget
	if err := n.DB.First(&t, "id = ? AND tenant_id = ?", d.TargetID, d.TenantID).Error; err != nil || t.Status != "active" {
		n.DB.Model(&model.FlowNotificationDelivery{}).Where("id = ?", d.ID).Updates(map[string]any{"status": "cancelled", "last_error": "target not active", "lease_until": nil, "updated_at": now})
		return
	}
	var receipt string
	var err error
	switch t.Channel {
	case "email":
		receipt, err = n.sendEmail(ctx, t, d)
	case "webhook":
		receipt, err = n.sendWebhook(ctx, t, d)
	default:
		err = fmt.Errorf("%w: channel %s", errPermanent, t.Channel)
	}
	upd := map[string]any{"lease_until": nil, "updated_at": now}
	switch {
	case err == nil:
		upd["status"], upd["receipt"], upd["delivered_at"], upd["last_error"] = "delivered", truncRunes(receipt, 300), now, ""
		_, _ = DefaultUsageRecorder.Record(n.DB, UsageInput{TenantID: d.TenantID, WorkspaceID: d.WorkspaceID, UsageType: UsageNotifications, Quantity: 1, CostMicro: -1,
			SubjectRef: t.Channel + ":" + d.ID, IdemKey: "notify:" + d.ID, Charge: true}, now)
	case errors.Is(err, errAmbiguous):
		upd["status"], upd["last_error"] = StatusReconciliation, truncRunes(err.Error(), 480)
	case errors.Is(err, errPermanent):
		upd["status"], upd["last_error"] = "failed", truncRunes(err.Error(), 480)
		// Target invalid: stop it; never re-route to another address.
		_ = n.DB.Transaction(func(tx *gorm.DB) error {
			if e := tx.Model(&model.FlowNotificationTarget{}).Where("id = ?", t.ID).Updates(map[string]any{"status": "disabled", "disabled_reason": truncRunes(err.Error(), 300), "updated_at": now}).Error; e != nil {
				return e
			}
			return cancelTargetDeliveries(tx, t.ID, "target disabled", now)
		})
	case d.Attempts >= d.MaxAttempts:
		upd["status"], upd["last_error"] = "failed", truncRunes(err.Error(), 480)
	default:
		backoff := n.Backoff
		if backoff <= 0 {
			backoff = 30 * time.Second
		}
		upd["status"], upd["last_error"], upd["next_attempt_at"] = StatusRetryWait, truncRunes(err.Error(), 480), now.Add(backoff*time.Duration(1<<(d.Attempts-1)))
	}
	n.DB.Model(&model.FlowNotificationDelivery{}).Where("id = ? AND status = ?", d.ID, "sending").Updates(upd)
}

func (n Notifier) message(d model.FlowNotificationDelivery) (subject, body string) {
	subject = "[WeaveSense-Flow] " + d.Title
	if d.Title == "" {
		subject = "[WeaveSense-Flow] " + d.EventType
	}
	link := ""
	if n.BaseURL != "" && d.RefID != "" {
		link = "\n" + strings.TrimRight(n.BaseURL, "/") + "/intelligence?report=" + url.QueryEscape(d.RefID)
	}
	return subject, fmt.Sprintf("%s\n\nEvent: %s\nReport: %s%s\nDelivery: %s\n", d.Title, d.EventType, d.RefID, link, d.ID)
}

func (n Notifier) sendEmail(ctx context.Context, t model.FlowNotificationTarget, d model.FlowNotificationDelivery) (string, error) {
	if n.Mail == nil {
		return "", errors.New("email delivery not configured (FLOW_NOTIFY_SMTP_HOST)")
	}
	subject, body := n.message(d)
	return n.Mail(ctx, t.Address, subject, body, map[string]string{"X-Flow-Delivery-Id": d.ID, "Message-ID": "<" + d.ID + "@weavesense-flow>"})
}

// sendWebhook POSTs a signed JSON payload. Receivers dedupe on X-Flow-Delivery-Id, so a
// lost response is safely retried with the same delivery id.
func (n Notifier) sendWebhook(ctx context.Context, t model.FlowNotificationTarget, d model.FlowNotificationDelivery) (string, error) {
	payload, _ := json.Marshal(map[string]any{"delivery_id": d.ID, "event_id": d.EventID, "type": d.EventType, "tenant_id": d.TenantID,
		"workspace_id": d.WorkspaceID, "title": d.Title, "report_id": d.RefID, "attempt": d.Attempts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Address, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("%w: %v", errPermanent, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Flow-Delivery-Id", d.ID)
	req.Header.Set("X-Flow-Event", d.EventType)
	if name := strings.TrimPrefix(t.SecretRef, "env:"); t.SecretRef != "" {
		secret := os.Getenv(name)
		if secret == "" {
			return "", fmt.Errorf("%w: webhook secret %s not configured", errPermanent, t.SecretRef)
		}
		m := hmac.New(sha256.New, []byte(secret))
		m.Write(payload)
		req.Header.Set("X-Flow-Signature", "sha256="+hex.EncodeToString(m.Sum(nil)))
	}
	hc := n.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second, Transport: PublicTransport(),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("webhook: %v", err) // retryable: receiver dedupes by delivery id
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return "http " + strconv.Itoa(resp.StatusCode), nil
	case resp.StatusCode == 404 || resp.StatusCode == 410 || resp.StatusCode == 401 || resp.StatusCode == 403:
		return "", fmt.Errorf("%w: webhook returned %d", errPermanent, resp.StatusCode)
	default:
		return "", fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
}

// SMTPMailerFromEnv: FLOW_NOTIFY_SMTP_HOST/_PORT/_USERNAME/_PASSWORD/_FROM/_ENCRYPTION (tls|starttls|none). nil when unset.
func SMTPMailerFromEnv() Mailer {
	host := strings.TrimSpace(os.Getenv("FLOW_NOTIFY_SMTP_HOST"))
	from := strings.TrimSpace(os.Getenv("FLOW_NOTIFY_SMTP_FROM"))
	if host == "" || from == "" {
		return nil
	}
	port, _ := strconv.Atoi(os.Getenv("FLOW_NOTIFY_SMTP_PORT"))
	if port == 0 {
		port = 587
	}
	user, pass, enc := os.Getenv("FLOW_NOTIFY_SMTP_USERNAME"), os.Getenv("FLOW_NOTIFY_SMTP_PASSWORD"), strings.ToLower(os.Getenv("FLOW_NOTIFY_SMTP_ENCRYPTION"))
	if enc == "" {
		enc = "starttls"
	}
	return SMTPMailer(host, port, user, pass, from, enc)
}

// SMTPMailer sends plain-text mail. Failures before DATA is accepted are retryable;
// a lost reply after the message body was sent is ambiguous (no blind resend).
func SMTPMailer(host string, port int, user, pass, from, enc string) Mailer {
	return func(ctx context.Context, to, subject, body string, headers map[string]string) (string, error) {
		addr := net.JoinHostPort(host, strconv.Itoa(port))
		d := net.Dialer{Timeout: 15 * time.Second}
		var conn net.Conn
		var err error
		if enc == "tls" {
			conn, err = tls.DialWithDialer(&d, "tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		} else {
			conn, err = d.DialContext(ctx, "tcp", addr)
		}
		if err != nil {
			return "", fmt.Errorf("smtp dial: %v", err)
		}
		_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
		c, err := smtp.NewClient(conn, host)
		if err != nil {
			conn.Close()
			return "", fmt.Errorf("smtp: %v", err)
		}
		defer c.Close()
		if enc == "starttls" {
			if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				return "", fmt.Errorf("smtp starttls: %v", err)
			}
		}
		if user != "" {
			if err := c.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
				return "", fmt.Errorf("smtp auth: %v", err)
			}
		}
		if err := c.Mail(from); err != nil {
			return "", fmt.Errorf("smtp mail: %v", err)
		}
		if err := c.Rcpt(to); err != nil {
			if strings.HasPrefix(err.Error(), "55") {
				return "", fmt.Errorf("%w: recipient rejected: %v", errPermanent, err)
			}
			return "", fmt.Errorf("smtp rcpt: %v", err)
		}
		w, err := c.Data()
		if err != nil {
			return "", fmt.Errorf("smtp data: %v", err)
		}
		var msg strings.Builder
		msg.WriteString("From: " + from + "\r\nTo: " + to + "\r\nSubject: " + mimeHeader(subject) + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n")
		for k, v := range headers {
			msg.WriteString(k + ": " + v + "\r\n")
		}
		msg.WriteString("\r\n" + strings.ReplaceAll(body, "\n", "\r\n"))
		if _, err := w.Write([]byte(msg.String())); err != nil {
			return "", fmt.Errorf("%w: smtp write: %v", errAmbiguous, err)
		}
		if err := w.Close(); err != nil {
			if strings.HasPrefix(err.Error(), "5") {
				return "", fmt.Errorf("%w: smtp rejected message: %v", errPermanent, err)
			}
			return "", fmt.Errorf("%w: smtp end-of-data: %v", errAmbiguous, err)
		}
		_ = c.Quit()
		return "smtp accepted " + headers["Message-ID"], nil
	}
}

func mimeHeader(s string) string {
	for _, r := range s {
		if r > 127 {
			return "=?UTF-8?B?" + base64Std(s) + "?="
		}
	}
	return s
}

// ResolveDelivery settles a reconciliation_required delivery after the operator checked
// the receiver: delivered=true records it, false re-queues it with the same delivery_id.
func ResolveDelivery(db *gorm.DB, s Scope, id string, delivered bool, now time.Time) (model.FlowNotificationDelivery, error) {
	var d model.FlowNotificationDelivery
	if err := db.First(&d, "id = ? AND tenant_id = ? AND workspace_id = ?", id, s.TenantID, s.WorkspaceID).Error; err != nil {
		return d, ErrNotFound
	}
	if d.Status != StatusReconciliation {
		return d, ErrBudgetState
	}
	upd := map[string]any{"status": StatusRetryWait, "next_attempt_at": now, "updated_at": now, "last_error": "reconciled: not delivered"}
	if delivered {
		upd = map[string]any{"status": "delivered", "delivered_at": now, "receipt": "reconciled by operator", "updated_at": now}
	}
	if err := db.Model(&d).Updates(upd).Error; err != nil {
		return d, err
	}
	return d, db.First(&d, "id = ?", id).Error
}

// RunNotifier delivers due notifications until ctx ends.
func RunNotifier(ctx context.Context, n Notifier, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := n.RunDue(ctx); err != nil {
			slog.Warn("flow: notifier", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

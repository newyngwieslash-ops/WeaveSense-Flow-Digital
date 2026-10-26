package edumath

import (
	"context"
	"log/slog"
	"os"
	"time"

	"gorm.io/gorm"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// SyncOnce advances every non-terminal (or unsettled) job: it pulls the service
// state, ingests artifacts, settles finished stages once, and reconciles unknown
// submits. Restart-safe: everything it needs is in the database.
func (s *Service) SyncOnce(ctx context.Context, limit int) (int, error) {
	var jobs []model.EduMathJob
	err := s.DB.Where("status IN ? OR settlement = ?", []string{"queued", "running", "reconciliation_required"}, "open").
		Order("updated_at").Limit(limit).Find(&jobs).Error
	if err != nil {
		return 0, err
	}
	n := 0
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		err := flow.TenantTx(s.DB, j.TenantID, func(tx *gorm.DB) error {
			var cur model.EduMathJob
			if err := tx.First(&cur, "id = ?", j.ID).Error; err != nil {
				return err
			}
			_, err := s.Sync(ctx, tx, cur)
			return err
		})
		if err != nil {
			slog.Warn("edumath sync failed", "job", j.ID, "error", err)
			continue
		}
		n++
	}
	return n, nil
}

// Run polls until ctx is cancelled.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		c, cancel := context.WithTimeout(ctx, every*10)
		_, _ = s.SyncOnce(c, 50)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// FromEnv builds the service from FLOW_MATH_SERVICE_URL / FLOW_MATH_SERVICE_KEY
// (and optional EDU_ARTIFACT_URL_KEY). Returns nil when not configured.
func FromEnv(db *gorm.DB) *Service {
	url, key := os.Getenv("FLOW_MATH_SERVICE_URL"), os.Getenv("FLOW_MATH_SERVICE_KEY")
	if url == "" || len(key) < 32 {
		return nil
	}
	uk := os.Getenv("EDU_ARTIFACT_URL_KEY")
	if len(uk) < 32 {
		uk = "edu-artifact-url:" + key
	}
	return &Service{DB: db, Client: Client{BaseURL: url, Key: []byte(key)}, URLKey: []byte(uk)}
}

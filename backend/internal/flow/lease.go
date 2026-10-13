package flow

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"yingce/backend/internal/model"
)

var ErrLeaseHeld = errors.New("flow: run lease held by another owner")

// RunLeases coordinates agent-run ownership across replicas.
type RunLeases struct {
	DB  *gorm.DB
	TTL time.Duration
	Now func() time.Time
}

func (l RunLeases) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l RunLeases) ttl() time.Duration {
	if l.TTL > 0 {
		return l.TTL
	}
	return 60 * time.Second
}

// Acquire takes the lease if it is free, expired, or already ours. It returns
// the fencing token the owner must present on renew/release.
func (l RunLeases) Acquire(runID, owner string) (int64, error) {
	now := l.now()
	exp := now.Add(l.ttl())
	if err := l.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.FlowRunLease{RunID: runID, Owner: owner, FencingToken: 1, ExpiresAt: exp}).Error; err != nil {
		return 0, err
	}
	r := l.DB.Model(&model.FlowRunLease{}).Where("run_id = ? AND (owner = ? OR expires_at < ?)", runID, owner, now).
		Updates(map[string]any{"owner": owner, "expires_at": exp, "fencing_token": gorm.Expr("CASE WHEN owner = ? THEN fencing_token ELSE fencing_token + 1 END", owner), "updated_at": now})
	if r.Error != nil {
		return 0, r.Error
	}
	if r.RowsAffected == 0 {
		return 0, ErrLeaseHeld
	}
	var row model.FlowRunLease
	if err := l.DB.First(&row, "run_id = ?", runID).Error; err != nil {
		return 0, err
	}
	return row.FencingToken, nil
}

// Renew extends the lease only for the current owner and fencing token.
func (l RunLeases) Renew(runID, owner string, token int64) error {
	now := l.now()
	r := l.DB.Model(&model.FlowRunLease{}).Where("run_id = ? AND owner = ? AND fencing_token = ?", runID, owner, token).
		Updates(map[string]any{"expires_at": now.Add(l.ttl()), "updated_at": now})
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected == 0 {
		return ErrLeaseHeld
	}
	return nil
}

// Release frees the lease so another replica may recover the run immediately.
func (l RunLeases) Release(runID, owner string, token int64) {
	l.DB.Where("run_id = ? AND owner = ? AND fencing_token = ?", runID, owner, token).Delete(&model.FlowRunLease{})
}

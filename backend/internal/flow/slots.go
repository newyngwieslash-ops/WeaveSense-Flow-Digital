package flow

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"yingce/backend/internal/model"
)

// RunSlots implements "runtime busy => queue": at most Limit items of a kind
// run per owner; the rest wait FIFO and are admitted as slots free up.
type RunSlots struct {
	DB    *gorm.DB
	Limit int
}

var ErrSlotQueued = errors.New("flow: runtime busy, queued")

// Acquire enqueues id (idempotent) and admits it if it is at the head of the
// queue and a slot is free. Returns admitted and 1-based queue position.
func (r RunSlots) Acquire(owner, kind, id, userID string) (bool, int, error) {
	if r.Limit <= 0 {
		return true, 0, nil
	}
	admitted, pos := false, 0
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.FlowRunSlot{ID: id, Owner: owner, Kind: kind, UserID: userID, Status: "queued", CreatedAt: time.Now()}).Error; err != nil {
			return err
		}
		var me model.FlowRunSlot
		if err := tx.First(&me, "id = ?", id).Error; err != nil {
			return err
		}
		if me.Status == "running" {
			admitted = true
			return nil
		}
		var running, ahead int64
		tx.Model(&model.FlowRunSlot{}).Where("owner = ? AND kind = ? AND status = 'running'", owner, kind).Count(&running)
		tx.Model(&model.FlowRunSlot{}).Where("owner = ? AND kind = ? AND status = 'queued' AND (created_at < ? OR (created_at = ? AND id < ?))", owner, kind, me.CreatedAt, me.CreatedAt, id).Count(&ahead)
		pos = int(ahead) + 1
		if int(running) < r.Limit && ahead == 0 {
			res := tx.Model(&model.FlowRunSlot{}).Where("id = ? AND status = 'queued'", id).Update("status", "running")
			admitted = res.RowsAffected == 1
			pos = 0
		}
		return nil
	})
	return admitted, pos, err
}

// Release frees id's slot (also used to cancel a queued item) and returns the
// queued items that are now at the head and fit, so the caller can start them.
func (r RunSlots) Release(id string) []model.FlowRunSlot {
	if r.Limit <= 0 {
		return nil
	}
	var me model.FlowRunSlot
	if r.DB.First(&me, "id = ?", id).Error != nil {
		return nil
	}
	r.DB.Delete(&model.FlowRunSlot{}, "id = ?", id)
	var next []model.FlowRunSlot
	r.DB.Where("owner = ? AND kind = ? AND status = 'queued'", me.Owner, me.Kind).Order("created_at, id").Limit(r.Limit).Find(&next)
	return next
}

// Wait blocks (polling) until id is admitted or ctx ends; used by synchronous
// executors such as research synthesis.
func (r RunSlots) Wait(ctx context.Context, owner, kind, id string) error {
	for {
		ok, _, err := r.Acquire(owner, kind, id, "")
		if err != nil || ok {
			return err
		}
		select {
		case <-ctx.Done():
			r.Release(id)
			return ErrSlotQueued
		case <-time.After(150 * time.Millisecond):
		}
	}
}

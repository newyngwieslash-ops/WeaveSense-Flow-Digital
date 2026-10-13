package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"yingce/backend/internal/flow"
)

var (
	piLeaseOwnerOnce sync.Once
	piLeaseOwnerID   string
)

func piLeaseOwner() string {
	piLeaseOwnerOnce.Do(func() {
		h, _ := os.Hostname()
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		piLeaseOwnerID = fmt.Sprintf("%s:%d:%s", h, os.Getpid(), hex.EncodeToString(b))
	})
	return piLeaseOwnerID
}

const piRunLeaseTTL = 60 * time.Second

// acquirePiRunLease takes the run lease and keeps renewing it until ctx ends.
// If a renewal fails (lease lost to another replica), cancel stops the session.
func (s *Service) acquirePiRunLease(ctx context.Context, cancel context.CancelFunc, runID string) (func(), error) {
	if s.repo == nil || s.repo.DB() == nil {
		return func() {}, nil
	}
	leases := flow.RunLeases{DB: s.repo.DB(), TTL: piRunLeaseTTL}
	owner := piLeaseOwner()
	tok, err := leases.Acquire(runID, owner)
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(piRunLeaseTTL / 3)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if err := leases.Renew(runID, owner, tok); err != nil {
					log.Printf("[Agent] run %s lease lost: %v", runID, err)
					cancel()
					return
				}
			}
		}
	}()
	return func() { close(done); leases.Release(runID, owner, tok) }, nil
}

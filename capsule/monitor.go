package capsule

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"
)

// GoroutineThreshold triggers only after observing a value below RearmBelow
// followed by a value at or above Threshold.
type GoroutineThreshold struct {
	Threshold  int
	RearmBelow int

	mu    sync.Mutex
	armed bool
}

func NewGoroutineThreshold(threshold, rearmBelow int) (*GoroutineThreshold, error) {
	if threshold <= 0 || rearmBelow < 0 || rearmBelow >= threshold {
		return nil, fmt.Errorf("require threshold > 0 and 0 <= rearmBelow < threshold")
	}
	return &GoroutineThreshold{Threshold: threshold, RearmBelow: rearmBelow}, nil
}

func (p *GoroutineThreshold) Observe(count int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if count < p.RearmBelow {
		p.armed = true
		return false
	}
	if p.armed && count >= p.Threshold {
		p.armed = false
		return true
	}
	return false
}

// WatchGoroutines samples runtime.NumGoroutine until ctx is canceled.
func (r *Recorder) WatchGoroutines(ctx context.Context, interval time.Duration, policy *GoroutineThreshold, reason string) error {
	if interval <= 0 || policy == nil {
		return fmt.Errorf("a positive interval and policy are required")
	}
	if err := ValidateReason(reason); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if policy.Observe(runtime.NumGoroutine()) {
				if _, err := r.Capture(ctx, reason); err != nil {
					return err
				}
			}
		}
	}
}

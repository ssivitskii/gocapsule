package capsule

import (
	"context"
	"fmt"
	"io"
	"runtime/trace"
	"sync"
	"time"
)

type flightRecorder interface {
	Start() error
	Stop()
	WriteTo(io.Writer) (int64, error)
}

// traceRecorder adapts runtime/trace without exposing it in Recorder's test seams.
type traceRecorder struct{ recorder *trace.FlightRecorder }

func (t traceRecorder) Start() error { return t.recorder.Start() }
func (t traceRecorder) Stop()        { t.recorder.Stop() }
func (t traceRecorder) WriteTo(w io.Writer) (int64, error) {
	return t.recorder.WriteTo(w)
}

var activeRecorders struct {
	sync.Mutex
	recorder *Recorder
}

type Recorder struct {
	cfg Config
	fr  flightRecorder

	stateMu sync.Mutex
	started bool
	closed  bool

	captureMu sync.Mutex
	lastTry   time.Time
	now       func() time.Time

	// captureHook is an intentionally unexported deterministic test seam.
	captureHook func(context.Context, string) ([]payload, error)
}

func NewRecorder(cfg Config) (*Recorder, error) {
	cfg = applyDefaults(cfg)
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	fr := trace.NewFlightRecorder(trace.FlightRecorderConfig{
		MinAge:   cfg.FlightRecorder.MinAge,
		MaxBytes: cfg.FlightRecorder.MaxBytes,
	})
	return &Recorder{
		cfg: cfg,
		fr:  traceRecorder{recorder: fr},
		now: time.Now,
	}, nil
}

func (r *Recorder) Start() error {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.closed {
		return ErrClosed
	}
	if r.started {
		return nil
	}

	activeRecorders.Lock()
	defer activeRecorders.Unlock()
	if activeRecorders.recorder != nil && activeRecorders.recorder != r {
		return ErrRecorderActive
	}
	if err := r.fr.Start(); err != nil {
		return fmt.Errorf("start runtime flight recorder: %w", err)
	}
	activeRecorders.recorder = r
	r.started = true
	return nil
}

func (r *Recorder) Capture(ctx context.Context, reason string) (CaptureResult, error) {
	if err := ValidateReason(reason); err != nil {
		return CaptureResult{}, err
	}
	if !r.captureMu.TryLock() {
		return CaptureResult{Outcome: OutcomeSuppressedProgress}, nil
	}
	defer r.captureMu.Unlock()

	r.stateMu.Lock()
	if r.closed {
		r.stateMu.Unlock()
		return CaptureResult{}, ErrClosed
	}
	if !r.started {
		r.stateMu.Unlock()
		return CaptureResult{}, ErrNotStarted
	}
	r.stateMu.Unlock()

	now := r.now().UTC()
	if !r.lastTry.IsZero() && now.Sub(r.lastTry) < r.cfg.Cooldown {
		return CaptureResult{Outcome: OutcomeSuppressedCooldown}, nil
	}
	// Cooldown starts when an attempt is accepted, even if collection later fails.
	r.lastTry = now

	if err := ctx.Err(); err != nil {
		return CaptureResult{}, err
	}
	collect := r.captureHook
	if collect == nil {
		collect = r.collect
	}
	payloads, err := collect(ctx, reason)
	if err != nil {
		return CaptureResult{}, err
	}
	path, err := r.writeArchive(now, reason, payloads)
	if err != nil {
		return CaptureResult{}, err
	}
	return CaptureResult{Outcome: OutcomeCaptured, Path: path}, nil
}

func (r *Recorder) Close() error {
	// Wait for an accepted capture so Stop cannot race with WriteTo.
	r.captureMu.Lock()
	defer r.captureMu.Unlock()
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.closed {
		return nil
	}
	if r.started {
		r.fr.Stop()
		activeRecorders.Lock()
		if activeRecorders.recorder == r {
			activeRecorders.recorder = nil
		}
		activeRecorders.Unlock()
	}
	r.started = false
	r.closed = true
	return nil
}

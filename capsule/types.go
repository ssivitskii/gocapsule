// Package capsule records bounded, local runtime incident snapshots.
package capsule

import (
	"errors"
	"time"
)

const (
	manifestSchema  = "gocapsule.manifest"
	manifestVersion = 1
	metricsSchema   = "gocapsule.metrics"
	metricsVersion  = 1
)

var (
	ErrRecorderActive = errors.New("another Go flight recorder is already active")
	ErrNotStarted     = errors.New("recorder is not started")
	ErrClosed         = errors.New("recorder is closed")
	ErrBudgetExceeded = errors.New("capture budget exceeded")
)

type Outcome string

const (
	OutcomeCaptured           Outcome = "captured"
	OutcomeSuppressedCooldown Outcome = "suppressed_cooldown"
	OutcomeSuppressedProgress Outcome = "suppressed_in_progress"
)

type CaptureResult struct {
	Outcome Outcome
	Path    string
}

type FlightRecorderConfig struct {
	// MinAge is the minimum history window the runtime should try to retain.
	MinAge time.Duration
	// MaxBytes is a runtime hint, not a hard memory or WriteTo bound.
	MaxBytes uint64
}

type Limits struct {
	TraceBytes             int64
	PayloadBytes           int64
	TotalUncompressedBytes int64
	ArchiveBytes           int64
}

type Config struct {
	OutputDir      string
	FlightRecorder FlightRecorderConfig
	Cooldown       time.Duration
	Profiles       []string
	Metrics        []string
	Limits         Limits
}

type Entry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Schema    string    `json:"schema"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	Reason    string    `json:"reason"`
	GoVersion string    `json:"go_version"`
	GOOS      string    `json:"goos"`
	GOARCH    string    `json:"goarch"`
	Entries   []Entry   `json:"entries"`
}

type VerifyLimits struct {
	ArchiveBytes           int64
	EntryBytes             int64
	TotalUncompressedBytes int64
}

func DefaultVerifyLimits() VerifyLimits {
	return VerifyLimits{
		ArchiveBytes:           64 << 20,
		EntryBytes:             64 << 20,
		TotalUncompressedBytes: 128 << 20,
	}
}

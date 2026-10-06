package capsule

import (
	"fmt"
	"runtime/metrics"
	"strings"
	"time"
)

var allowedProfiles = map[string]struct{}{
	"allocs": {}, "block": {}, "goroutine": {}, "heap": {}, "mutex": {}, "threadcreate": {},
}

var defaultMetrics = []string{
	"/gc/heap/live:bytes",
	"/memory/classes/heap/objects:bytes",
	"/sched/goroutines:goroutines",
}

func applyDefaults(cfg Config) Config {
	if cfg.OutputDir == "" {
		cfg.OutputDir = "gocapsules"
	}
	if cfg.FlightRecorder.MinAge == 0 {
		cfg.FlightRecorder.MinAge = 10 * time.Second
	}
	if cfg.FlightRecorder.MaxBytes == 0 {
		cfg.FlightRecorder.MaxBytes = 8 << 20
	}
	if cfg.Cooldown == 0 {
		cfg.Cooldown = time.Minute
	}
	if cfg.Profiles == nil {
		cfg.Profiles = []string{"goroutine", "heap"}
	}
	if cfg.Metrics == nil {
		cfg.Metrics = append([]string(nil), defaultMetrics...)
	}
	if cfg.Limits.TraceBytes == 0 {
		cfg.Limits.TraceBytes = 32 << 20
	}
	if cfg.Limits.PayloadBytes == 0 {
		cfg.Limits.PayloadBytes = 16 << 20
	}
	if cfg.Limits.TotalUncompressedBytes == 0 {
		cfg.Limits.TotalUncompressedBytes = 64 << 20
	}
	if cfg.Limits.ArchiveBytes == 0 {
		cfg.Limits.ArchiveBytes = 32 << 20
	}
	return cfg
}

func validateConfig(cfg Config) error {
	if cfg.FlightRecorder.MinAge < 0 || cfg.Cooldown < 0 {
		return fmt.Errorf("durations must not be negative")
	}
	if cfg.Limits.TraceBytes <= 0 || cfg.Limits.PayloadBytes <= 0 || cfg.Limits.TotalUncompressedBytes <= 0 || cfg.Limits.ArchiveBytes <= 0 {
		return fmt.Errorf("all limits must be positive")
	}
	seen := make(map[string]struct{}, len(cfg.Profiles))
	for _, name := range cfg.Profiles {
		if _, ok := allowedProfiles[name]; !ok {
			return fmt.Errorf("unsupported profile %q", name)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicate profile %q", name)
		}
		seen[name] = struct{}{}
	}

	descriptions := make(map[string]metrics.ValueKind)
	for _, d := range metrics.All() {
		descriptions[d.Name] = d.Kind
	}
	seen = make(map[string]struct{}, len(cfg.Metrics))
	for _, name := range cfg.Metrics {
		kind, ok := descriptions[name]
		if !ok {
			return fmt.Errorf("unknown runtime metric %q", name)
		}
		if kind != metrics.KindUint64 && kind != metrics.KindFloat64 {
			return fmt.Errorf("runtime metric %q is not scalar", name)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicate runtime metric %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func ValidateReason(reason string) error {
	if len(reason) == 0 || len(reason) > 64 {
		return fmt.Errorf("reason must contain 1 to 64 characters")
	}
	for i := 0; i < len(reason); i++ {
		c := reason[i]
		if i == 0 {
			if c < 'a' || c > 'z' {
				return fmt.Errorf("reason must start with a lowercase ASCII letter")
			}
			continue
		}
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || strings.ContainsRune("._-", rune(c)) {
			continue
		}
		return fmt.Errorf("reason contains unsupported character at byte %d", i)
	}
	return nil
}

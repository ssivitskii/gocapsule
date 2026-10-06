package capsule

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"testing"
	"time"
)

type fakeFlightRecorder struct {
	startErr error
	writeErr error
	data     []byte
}

func (f *fakeFlightRecorder) Start() error { return f.startErr }
func (f *fakeFlightRecorder) Stop()        {}
func (f *fakeFlightRecorder) WriteTo(w io.Writer) (int64, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	n, err := w.Write(f.data)
	return int64(n), err
}

func minimalConfig(dir string) Config {
	return Config{
		OutputDir: dir,
		Profiles:  []string{},
		Metrics:   []string{},
		Cooldown:  time.Minute,
		Limits: Limits{
			TraceBytes: 1 << 20, PayloadBytes: 1 << 20,
			TotalUncompressedBytes: 2 << 20, ArchiveBytes: 2 << 20,
		},
	}
}

func testRecorder(t *testing.T, cfg Config) *Recorder {
	t.Helper()
	recorder, err := NewRecorder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	recorder.fr = &fakeFlightRecorder{data: []byte("trace")}
	if err := recorder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	return recorder
}

func hookPayloads() []payload {
	return []payload{{name: "trace.out", data: validTraceData()}, {name: "metrics.json", data: []byte(`{"schema":"gocapsule.metrics","version":1,"values":[]}`)}}
}

func validTraceData() []byte { return []byte("go 1.25 trace\x00\x00\x00payload") }

func TestRecorderLifecycleAndGlobalExclusivity(t *testing.T) {
	first, err := NewRecorder(minimalConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	first.fr = &fakeFlightRecorder{}
	second, err := NewRecorder(minimalConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	second.fr = &fakeFlightRecorder{}

	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(); !errors.Is(err, ErrRecorderActive) {
		t.Fatalf("second Start error = %v, want ErrRecorderActive", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(); err != nil {
		t.Fatalf("Start after release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start after Close error = %v, want ErrClosed", err)
	}
}

func TestConcurrentCaptureIsSuppressed(t *testing.T) {
	recorder := testRecorder(t, minimalConfig(t.TempDir()))
	entered := make(chan struct{})
	release := make(chan struct{})
	recorder.captureHook = func(context.Context, string) ([]payload, error) {
		close(entered)
		<-release
		return hookPayloads(), nil
	}

	results := make(chan CaptureResult, 1)
	errs := make(chan error, 1)
	go func() {
		result, err := recorder.Capture(context.Background(), "test.concurrent")
		results <- result
		errs <- err
	}()
	<-entered
	second, err := recorder.Capture(context.Background(), "test.concurrent")
	if err != nil {
		t.Fatal(err)
	}
	if second.Outcome != OutcomeSuppressedProgress {
		t.Fatalf("second outcome = %q", second.Outcome)
	}
	close(release)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if result := <-results; result.Outcome != OutcomeCaptured {
		t.Fatalf("first outcome = %q", result.Outcome)
	}
}

func TestCooldownStartsOnFailedAttempt(t *testing.T) {
	recorder := testRecorder(t, minimalConfig(t.TempDir()))
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	recorder.now = func() time.Time { return now }
	calls := 0
	recorder.captureHook = func(context.Context, string) ([]payload, error) {
		calls++
		return nil, errors.New("collection failed")
	}
	if _, err := recorder.Capture(context.Background(), "test.failure"); err == nil {
		t.Fatal("expected first capture to fail")
	}
	result, err := recorder.Capture(context.Background(), "test.failure")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeSuppressedCooldown || calls != 1 {
		t.Fatalf("outcome=%q calls=%d", result.Outcome, calls)
	}
	now = now.Add(time.Minute)
	if _, err := recorder.Capture(context.Background(), "test.failure"); err == nil || calls != 2 {
		t.Fatalf("retry error=%v calls=%d", err, calls)
	}
}

func TestReasonValidation(t *testing.T) {
	valid := []string{"a", "worker.queue-depth", "reason_2", "a-b"}
	for _, reason := range valid {
		if err := ValidateReason(reason); err != nil {
			t.Errorf("ValidateReason(%q): %v", reason, err)
		}
	}
	invalid := []string{"", "2bad", "Bad", "has space", "slash/no", "é", "a!", "a" + string(bytes.Repeat([]byte{'x'}, 64))}
	for _, reason := range invalid {
		if err := ValidateReason(reason); err == nil {
			t.Errorf("ValidateReason(%q) succeeded", reason)
		}
	}
}

func TestCaptureArchiveVerifiesAndUsesPrivatePermissions(t *testing.T) {
	parent := t.TempDir()
	dir := parent + "/new-private-dir"
	cfg := Config{OutputDir: dir, Cooldown: time.Nanosecond}
	recorder, err := NewRecorder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	recorder.fr = &fakeFlightRecorder{data: validTraceData()}
	if err := recorder.Start(); err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	result, err := recorder.Capture(context.Background(), "test.archive")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := Verify(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) != 4 {
		t.Fatalf("entry count = %d, want trace + metrics + two profiles", len(manifest.Entries))
	}
	if runtime.GOOS != "windows" {
		dirInfo, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		fileInfo, err := os.Stat(result.Path)
		if err != nil {
			t.Fatal(err)
		}
		if got := dirInfo.Mode().Perm(); got != 0o700 {
			t.Fatalf("directory permissions = %o", got)
		}
		if got := fileInfo.Mode().Perm(); got != 0o600 {
			t.Fatalf("archive permissions = %o", got)
		}
	}
}

func TestArchiveBudgetFailureCleansTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	cfg := minimalConfig(dir)
	cfg.Limits.ArchiveBytes = 10
	recorder := testRecorder(t, cfg)
	recorder.captureHook = func(context.Context, string) ([]payload, error) { return hookPayloads(), nil }
	if _, err := recorder.Capture(context.Background(), "test.budget"); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("Capture error = %v, want ErrBudgetExceeded", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("left files after failure: %v", entries)
	}
}

func TestCollectionEnforcesSharedUncompressedBudget(t *testing.T) {
	dir := t.TempDir()
	cfg := minimalConfig(dir)
	cfg.Limits.TotalUncompressedBytes = int64(len(validTraceData()) + 8)
	recorder := testRecorder(t, cfg)
	recorder.fr = &fakeFlightRecorder{data: validTraceData()}
	if _, err := recorder.Capture(context.Background(), "test.total-budget"); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("Capture error = %v, want ErrBudgetExceeded", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("collection failure left files: %v", entries)
	}
}

func TestInvalidConfiguration(t *testing.T) {
	for _, cfg := range []Config{
		{Profiles: []string{"cpu"}},
		{Metrics: []string{"/not/a/metric:bytes"}},
		{Metrics: []string{"/sched/latencies:seconds"}},
	} {
		if _, err := NewRecorder(cfg); err == nil {
			t.Fatalf("NewRecorder(%+v) succeeded", cfg)
		}
	}
}

func BenchmarkFlightRecorderState(b *testing.B) {
	b.Run("disabled", func(b *testing.B) {
		for range b.N {
			runtime.Gosched()
		}
	})
	b.Run("active", func(b *testing.B) {
		recorder, err := NewRecorder(minimalConfig(b.TempDir()))
		if err != nil {
			b.Fatal(err)
		}
		if err := recorder.Start(); err != nil {
			b.Fatal(err)
		}
		defer recorder.Close()
		b.ResetTimer()
		for range b.N {
			runtime.Gosched()
		}
	})
}

func TestCaptureRequiresStart(t *testing.T) {
	recorder, err := NewRecorder(minimalConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.Capture(context.Background(), "test.not-started"); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("Capture error = %v", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.Capture(context.Background(), "test.closed"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Capture after Close error = %v", err)
	}
}

func TestStartFailureDoesNotClaimGlobalRecorder(t *testing.T) {
	bad, err := NewRecorder(minimalConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	bad.fr = &fakeFlightRecorder{startErr: fmt.Errorf("boom")}
	if err := bad.Start(); err == nil {
		t.Fatal("Start succeeded")
	}
	good := testRecorder(t, minimalConfig(t.TempDir()))
	if good == nil {
		t.Fatal("good recorder is nil")
	}
}

package capsule

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRealCaptureWorksWithStandardGoTools(t *testing.T) {
	archiveDir := t.TempDir()
	recorder, err := NewRecorder(Config{OutputDir: archiveDir, Cooldown: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Start(); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	group.Add(32)
	for range 32 {
		go func() {
			defer group.Done()
			for range 32 {
				runtime.Gosched()
			}
		}()
	}
	group.Wait()
	result, err := recorder.Capture(context.Background(), "test.go-tools")
	closeErr := recorder.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if _, err := Verify(result.Path); err != nil {
		t.Fatal(err)
	}

	analysisDir := t.TempDir()
	files, err := extractSelected(result.Path, analysisDir, "trace.out", "profiles/heap.pb.gz")
	if err != nil {
		t.Fatal(err)
	}
	runGoTool(t, "pprof", "-top", files["profiles/heap.pb.gz"])
	runGoTool(t, "trace", "-pprof=sched", files["trace.out"])
}

func runGoTool(t *testing.T, name string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"tool", name}, args...)
	output, err := exec.Command("go", commandArgs...).CombinedOutput()
	if strings.Contains(string(output), "no such tool") {
		t.Skipf("go tool %s unavailable: %s", name, strings.TrimSpace(string(output)))
	}
	if err != nil {
		t.Fatalf("go tool %s failed: %v\n%s", name, err, output)
	}
}

func extractSelected(archivePath, destination string, names ...string) (map[string]string, error) {
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		wanted[name] = struct{}{}
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	result := make(map[string]string, len(names))
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if _, ok := wanted[header.Name]; !ok {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("selected entry %q is not regular", header.Name)
		}
		outputPath := filepath.Join(destination, filepath.Base(header.Name))
		output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		_, copyErr := io.Copy(output, tr)
		closeErr := output.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		result[header.Name] = outputPath
	}
	for name := range wanted {
		if _, ok := result[name]; !ok {
			return nil, fmt.Errorf("selected entry %q is missing", name)
		}
	}
	return result, nil
}

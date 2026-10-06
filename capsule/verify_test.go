package capsule

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type customTarEntry struct {
	header tar.Header
	data   []byte
}

func validTestManifest(entries []customTarEntry) Manifest {
	manifest := Manifest{
		Schema: manifestSchema, Version: manifestVersion,
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Reason:    "test.verify", GoVersion: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
	}
	for _, item := range entries {
		if item.header.Name == "manifest.json" || item.header.Typeflag == tar.TypeSymlink {
			continue
		}
		sum := sha256.Sum256(item.data)
		manifest.Entries = append(manifest.Entries, Entry{
			Name: item.header.Name, Size: int64(len(item.data)), SHA256: hex.EncodeToString(sum[:]),
		})
	}
	return manifest
}

func writeCustomArchive(t *testing.T, manifest Manifest, entries []customTarEntry) string {
	t.Helper()
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return writeRawCustomArchive(t, manifestData, entries)
}

func writeRawCustomArchive(t *testing.T, manifestData []byte, entries []customTarEntry) string {
	return writeRawCustomArchiveWithTrailing(t, manifestData, entries, nil)
}

func writeRawCustomArchiveWithTrailing(t *testing.T, manifestData []byte, entries []customTarEntry, trailing []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.tar.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	all := append([]customTarEntry{{header: tar.Header{Name: "manifest.json", Mode: 0o600}, data: manifestData}}, entries...)
	for _, item := range all {
		header := item.header
		if header.Typeflag == 0 {
			header.Typeflag = tar.TypeReg
		}
		header.Size = int64(len(item.data))
		if err := tw.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(item.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := gz.Write(trailing); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestVerifyRejectsMalformedArchives(t *testing.T) {
	baseEntry := customTarEntry{header: tar.Header{Name: "trace.out", Mode: 0o600}, data: validTraceData()}
	metricsEntry := customTarEntry{header: tar.Header{Name: "metrics.json", Mode: 0o600}, data: []byte(`{"schema":"gocapsule.metrics","version":1,"values":[]}`)}
	coreEntries := []customTarEntry{baseEntry, metricsEntry}

	t.Run("valid core", func(t *testing.T) {
		path := writeCustomArchive(t, validTestManifest(coreEntries), coreEntries)
		if _, err := Verify(path); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("checksum", func(t *testing.T) {
		manifest := validTestManifest([]customTarEntry{baseEntry})
		manifest.Entries[0].SHA256 = string(make([]byte, 64))
		path := writeCustomArchive(t, manifest, []customTarEntry{baseEntry})
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("duplicate", func(t *testing.T) {
		manifest := validTestManifest([]customTarEntry{baseEntry})
		path := writeCustomArchive(t, manifest, []customTarEntry{baseEntry, baseEntry})
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("path traversal", func(t *testing.T) {
		bad := customTarEntry{header: tar.Header{Name: "../escape", Mode: 0o600}, data: []byte("bad")}
		path := writeCustomArchive(t, validTestManifest([]customTarEntry{bad}), []customTarEntry{bad})
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("absolute path", func(t *testing.T) {
		bad := customTarEntry{header: tar.Header{Name: "/escape", Mode: 0o600}, data: []byte("bad")}
		path := writeCustomArchive(t, validTestManifest([]customTarEntry{bad}), []customTarEntry{bad})
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("windows absolute path", func(t *testing.T) {
		bad := customTarEntry{header: tar.Header{Name: "C:/escape", Mode: 0o600}, data: []byte("bad")}
		path := writeCustomArchive(t, validTestManifest([]customTarEntry{bad}), []customTarEntry{bad})
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		link := customTarEntry{header: tar.Header{Name: "trace.out", Typeflag: tar.TypeSymlink, Linkname: "/tmp/target"}}
		path := writeCustomArchive(t, validTestManifest(nil), []customTarEntry{link})
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("missing", func(t *testing.T) {
		manifest := validTestManifest([]customTarEntry{baseEntry})
		path := writeCustomArchive(t, manifest, nil)
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("unlisted", func(t *testing.T) {
		path := writeCustomArchive(t, validTestManifest(nil), []customTarEntry{baseEntry})
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("entry oversized", func(t *testing.T) {
		manifest := validTestManifest([]customTarEntry{baseEntry})
		path := writeCustomArchive(t, manifest, []customTarEntry{baseEntry})
		limits := DefaultVerifyLimits()
		limits.EntryBytes = 4
		if _, err := Verify(path, limits); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("uncompressed total oversized", func(t *testing.T) {
		manifest := validTestManifest([]customTarEntry{baseEntry})
		path := writeCustomArchive(t, manifest, []customTarEntry{baseEntry})
		limits := DefaultVerifyLimits()
		limits.TotalUncompressedBytes = 10
		if _, err := Verify(path, limits); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("compressed oversized", func(t *testing.T) {
		manifest := validTestManifest([]customTarEntry{baseEntry})
		path := writeCustomArchive(t, manifest, []customTarEntry{baseEntry})
		limits := DefaultVerifyLimits()
		limits.ArchiveBytes = 1
		if _, err := Verify(path, limits); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("invalid schema", func(t *testing.T) {
		manifest := validTestManifest([]customTarEntry{baseEntry})
		manifest.Version++
		path := writeCustomArchive(t, manifest, []customTarEntry{baseEntry})
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("empty manifest", func(t *testing.T) {
		path := writeCustomArchive(t, validTestManifest(nil), nil)
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("missing trace", func(t *testing.T) {
		path := writeCustomArchive(t, validTestManifest([]customTarEntry{metricsEntry}), []customTarEntry{metricsEntry})
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("missing metrics", func(t *testing.T) {
		path := writeCustomArchive(t, validTestManifest([]customTarEntry{baseEntry}), []customTarEntry{baseEntry})
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("invalid trace header", func(t *testing.T) {
		badTrace := customTarEntry{header: tar.Header{Name: "trace.out", Mode: 0o600}, data: []byte("not a Go trace")}
		entries := []customTarEntry{badTrace, metricsEntry}
		path := writeCustomArchive(t, validTestManifest(entries), entries)
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("portable future metric", func(t *testing.T) {
		futureMetrics := customTarEntry{header: tar.Header{Name: "metrics.json", Mode: 0o600}, data: []byte(`{"schema":"gocapsule.metrics","version":1,"values":[{"name":"/future/cache/hits:events","kind":"uint64","uint64":1}]}`)}
		entries := []customTarEntry{baseEntry, futureMetrics}
		path := writeCustomArchive(t, validTestManifest(entries), entries)
		if _, err := Verify(path); err != nil {
			t.Fatal(err)
		}
	})

	for name, data := range map[string]string{
		"missing slash": `{"schema":"gocapsule.metrics","version":1,"values":[{"name":"future:events","kind":"uint64","uint64":1}]}`,
		"control":       `{"schema":"gocapsule.metrics","version":1,"values":[{"name":"/future/cache:events\u0001","kind":"uint64","uint64":1}]}`,
		"invalid utf8":  "{\"schema\":\"gocapsule.metrics\",\"version\":1,\"values\":[{\"name\":\"/future/\xff:events\",\"kind\":\"uint64\",\"uint64\":1}]}",
		"wrong scalar":  `{"schema":"gocapsule.metrics","version":1,"values":[{"name":"/future/cache:events","kind":"uint64","float64":1}]}`,
		"both scalars":  `{"schema":"gocapsule.metrics","version":1,"values":[{"name":"/future/cache:events","kind":"uint64","uint64":1,"float64":1}]}`,
	} {
		t.Run("invalid metric "+name, func(t *testing.T) {
			badMetrics := customTarEntry{header: tar.Header{Name: "metrics.json", Mode: 0o600}, data: []byte(data)}
			entries := []customTarEntry{baseEntry, badMetrics}
			path := writeCustomArchive(t, validTestManifest(entries), entries)
			if _, err := Verify(path); err == nil {
				t.Fatal("Verify succeeded")
			}
		})
	}

	t.Run("duplicate metrics", func(t *testing.T) {
		name := "/sched/goroutines:goroutines"
		data := fmt.Sprintf(`{"schema":"gocapsule.metrics","version":1,"values":[{"name":%q,"kind":"uint64","uint64":1},{"name":%q,"kind":"uint64","uint64":2}]}`, name, name)
		badMetrics := customTarEntry{header: tar.Header{Name: "metrics.json", Mode: 0o600}, data: []byte(data)}
		entries := []customTarEntry{baseEntry, badMetrics}
		path := writeCustomArchive(t, validTestManifest(entries), entries)
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("too many metrics", func(t *testing.T) {
		values := make([]metricValue, 0, maxMetricValues+1)
		for i := 0; i <= maxMetricValues; i++ {
			value := uint64(i)
			values = append(values, metricValue{Name: fmt.Sprintf("/future/value-%d:events", i), Kind: "uint64", Uint64: &value})
		}
		data, err := json.Marshal(metricsDocument{Schema: metricsSchema, Version: metricsVersion, Values: values})
		if err != nil {
			t.Fatal(err)
		}
		tooMany := customTarEntry{header: tar.Header{Name: "metrics.json", Mode: 0o600}, data: data}
		entries := []customTarEntry{baseEntry, tooMany}
		path := writeCustomArchive(t, validTestManifest(entries), entries)
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("oversized metrics json", func(t *testing.T) {
		data := append([]byte(`{"schema":"gocapsule.metrics","version":1,"values":[]}`), bytes.Repeat([]byte{' '}, maxMetricsBytes)...)
		oversized := customTarEntry{header: tar.Header{Name: "metrics.json", Mode: 0o600}, data: data}
		entries := []customTarEntry{baseEntry, oversized}
		path := writeCustomArchive(t, validTestManifest(entries), entries)
		if _, err := Verify(path); !errors.Is(err, ErrBudgetExceeded) {
			t.Fatalf("Verify error = %v, want ErrBudgetExceeded", err)
		}
	})

	t.Run("invalid profile", func(t *testing.T) {
		profile := customTarEntry{header: tar.Header{Name: "profiles/heap.pb.gz", Mode: 0o600}, data: []byte("not gzip")}
		entries := append(append([]customTarEntry(nil), coreEntries...), profile)
		path := writeCustomArchive(t, validTestManifest(entries), entries)
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("valid nested profile gzip", func(t *testing.T) {
		profile := customTarEntry{header: tar.Header{Name: "profiles/heap.pb.gz", Mode: 0o600}, data: gzipZeros(t, 1024)}
		entries := append(append([]customTarEntry(nil), coreEntries...), profile)
		path := writeCustomArchive(t, validTestManifest(entries), entries)
		if _, err := Verify(path); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("corrupt nested profile trailer", func(t *testing.T) {
		data := gzipZeros(t, 1024)
		data[len(data)-1] ^= 0xff
		profile := customTarEntry{header: tar.Header{Name: "profiles/heap.pb.gz", Mode: 0o600}, data: data}
		entries := append(append([]customTarEntry(nil), coreEntries...), profile)
		path := writeCustomArchive(t, validTestManifest(entries), entries)
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify accepted corrupt nested gzip trailer")
		}
	})

	t.Run("nested profile gzip bomb", func(t *testing.T) {
		profile := customTarEntry{header: tar.Header{Name: "profiles/heap.pb.gz", Mode: 0o600}, data: gzipZeros(t, maxProfileDecompressedBytes+1)}
		entries := append(append([]customTarEntry(nil), coreEntries...), profile)
		path := writeCustomArchive(t, validTestManifest(entries), entries)
		if _, err := Verify(path); !errors.Is(err, ErrBudgetExceeded) {
			t.Fatalf("Verify error = %v, want ErrBudgetExceeded", err)
		}
	})

	t.Run("oversized manifest", func(t *testing.T) {
		path := writeRawCustomArchive(t, bytes.Repeat([]byte{' '}, maxManifestBytes+1), coreEntries)
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("too many manifest entries", func(t *testing.T) {
		manifest := validTestManifest(nil)
		for i := 0; i <= maxArchiveEntries; i++ {
			manifest.Entries = append(manifest.Entries, Entry{Name: fmt.Sprintf("item-%d", i), SHA256: strings.Repeat("0", 64)})
		}
		path := writeCustomArchive(t, manifest, nil)
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify succeeded")
		}
	})

	t.Run("oversized pax metadata", func(t *testing.T) {
		traceWithPAX := baseEntry
		traceWithPAX.header.PAXRecords = map[string]string{"comment": strings.Repeat("x", 550<<10)}
		metricsWithPAX := metricsEntry
		metricsWithPAX.header.PAXRecords = map[string]string{"comment": strings.Repeat("y", 550<<10)}
		entries := []customTarEntry{traceWithPAX, metricsWithPAX}
		path := writeCustomArchive(t, validTestManifest(entries), entries)
		limits := DefaultVerifyLimits()
		limits.TotalUncompressedBytes = 1 << 10
		if _, err := Verify(path, limits); !errors.Is(err, ErrBudgetExceeded) {
			t.Fatalf("Verify error = %v, want ErrBudgetExceeded", err)
		}
	})

	t.Run("corrupt gzip trailer", func(t *testing.T) {
		path := writeCustomArchive(t, validTestManifest(coreEntries), coreEntries)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data[len(data)-1] ^= 0xff
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify accepted corrupt gzip trailer")
		}
	})

	t.Run("zero tar padding accepted", func(t *testing.T) {
		manifestData, err := json.Marshal(validTestManifest(coreEntries))
		if err != nil {
			t.Fatal(err)
		}
		path := writeRawCustomArchiveWithTrailing(t, manifestData, coreEntries, make([]byte, 4096))
		if _, err := Verify(path); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("non-zero trailing data", func(t *testing.T) {
		manifestData, err := json.Marshal(validTestManifest(coreEntries))
		if err != nil {
			t.Fatal(err)
		}
		path := writeRawCustomArchiveWithTrailing(t, manifestData, coreEntries, []byte{1})
		if _, err := Verify(path); err == nil {
			t.Fatal("Verify accepted non-zero trailing data")
		}
	})
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func gzipZeros(t *testing.T, size int64) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	if _, err := io.CopyN(gz, zeroReader{}, size); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

package capsule

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxArchiveEntries           = 128
	maxManifestBytes            = 256 << 10
	maxMetricsBytes             = 256 << 10
	maxMetricValues             = 512
	maxMetricNameBytes          = 256
	maxProfileDecompressedBytes = 64 << 20
	verifyOverheadBytes         = 1 << 20
)

type observedEntry struct {
	size int64
	hash string
}

// Verify validates an archive without extracting it. Checksums detect corruption;
// they do not authenticate an archive from an untrusted source.
func Verify(archivePath string, optional ...VerifyLimits) (*Manifest, error) {
	limits := DefaultVerifyLimits()
	if len(optional) > 1 {
		return nil, fmt.Errorf("at most one VerifyLimits value is allowed")
	}
	if len(optional) == 1 {
		limits = optional[0]
	}
	if limits.ArchiveBytes <= 0 || limits.EntryBytes <= 0 || limits.TotalUncompressedBytes <= 0 {
		return nil, fmt.Errorf("verify limits must be positive")
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("fstat archive: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("archive is not a regular file")
	}
	if info.Size() > limits.ArchiveBytes {
		return nil, fmt.Errorf("compressed archive: %w", ErrBudgetExceeded)
	}
	compressed := &boundedReader{reader: file, remaining: limits.ArchiveBytes}
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return nil, fmt.Errorf("open gzip stream: %w", err)
	}
	defer gz.Close()
	if limits.TotalUncompressedBytes > math.MaxInt64-verifyOverheadBytes {
		return nil, fmt.Errorf("uncompressed limit is too large")
	}
	stream := &boundedReader{reader: gz, remaining: limits.TotalUncompressedBytes + verifyOverheadBytes}
	tr := tar.NewReader(stream)

	seen := make(map[string]observedEntry)
	var manifest *Manifest
	var total int64
	entryCount := 0
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar header: %w", err)
		}
		entryCount++
		if entryCount > maxArchiveEntries {
			return nil, fmt.Errorf("too many archive entries")
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("entry %q is not a regular file", header.Name)
		}
		if !validArchivePath(header.Name) {
			return nil, fmt.Errorf("unsafe archive path %q", header.Name)
		}
		if _, ok := seen[header.Name]; ok || (header.Name == "manifest.json" && manifest != nil) {
			return nil, fmt.Errorf("duplicate archive entry %q", header.Name)
		}
		if header.Size < 0 || header.Size > limits.EntryBytes {
			return nil, fmt.Errorf("entry %q: %w", header.Name, ErrBudgetExceeded)
		}
		if header.Size > limits.TotalUncompressedBytes-total {
			return nil, fmt.Errorf("uncompressed archive: %w", ErrBudgetExceeded)
		}
		total += header.Size

		if header.Name == "manifest.json" {
			if header.Size > maxManifestBytes {
				return nil, fmt.Errorf("manifest.json: %w", ErrBudgetExceeded)
			}
			data, err := io.ReadAll(io.LimitReader(tr, header.Size+1))
			if err != nil {
				return nil, fmt.Errorf("read manifest: %w", err)
			}
			if int64(len(data)) != header.Size {
				return nil, fmt.Errorf("manifest size mismatch")
			}
			var decoded Manifest
			decoder := json.NewDecoder(strings.NewReader(string(data)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&decoded); err != nil {
				return nil, fmt.Errorf("decode manifest: %w", err)
			}
			if err := ensureJSONEOF(decoder); err != nil {
				return nil, fmt.Errorf("decode manifest: %w", err)
			}
			manifest = &decoded
			continue
		}

		hash := sha256.New()
		limited := &io.LimitedReader{R: tr, N: header.Size}
		switch {
		case header.Name == "metrics.json":
			if header.Size > maxMetricsBytes {
				return nil, fmt.Errorf("metrics.json: %w", ErrBudgetExceeded)
			}
			err = validateMetrics(io.TeeReader(limited, hash))
		case header.Name == "trace.out":
			prefix := &prefixWriter{max: traceHeaderSize}
			_, err = io.Copy(io.MultiWriter(hash, prefix), limited)
			if err == nil {
				err = validateTraceHeader(prefix.bytes)
			}
		case strings.HasPrefix(header.Name, "profiles/"):
			if err = validateProfileName(header.Name); err == nil {
				err = validateProfileGzip(io.TeeReader(limited, hash))
			}
		default:
			err = fmt.Errorf("unsupported schema-v1 payload %q", header.Name)
		}
		if err != nil {
			return nil, fmt.Errorf("read entry %q: %w", header.Name, err)
		}
		n := header.Size - limited.N
		if n != header.Size {
			return nil, fmt.Errorf("entry %q size mismatch", header.Name)
		}
		seen[header.Name] = observedEntry{size: n, hash: hex.EncodeToString(hash.Sum(nil))}
	}
	if err := consumeZeroPadding(stream); err != nil {
		return nil, err
	}
	if manifest == nil {
		return nil, fmt.Errorf("manifest.json is missing")
	}
	if err := validateManifest(*manifest, seen); err != nil {
		return nil, err
	}
	return manifest, nil
}

// Inspect is Verify with the same bounded, non-extracting behavior.
func Inspect(archivePath string, optional ...VerifyLimits) (*Manifest, error) {
	return Verify(archivePath, optional...)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("unexpected data after JSON document")
}

func validateManifest(manifest Manifest, seen map[string]observedEntry) error {
	if manifest.Schema != manifestSchema || manifest.Version != manifestVersion {
		return fmt.Errorf("unsupported manifest schema %q version %d", manifest.Schema, manifest.Version)
	}
	if manifest.CreatedAt.IsZero() || manifest.GoVersion == "" || manifest.GOOS == "" || manifest.GOARCH == "" {
		return fmt.Errorf("manifest metadata is incomplete")
	}
	if err := ValidateReason(manifest.Reason); err != nil {
		return fmt.Errorf("invalid manifest reason: %w", err)
	}
	if len(manifest.Entries) > maxArchiveEntries {
		return fmt.Errorf("manifest contains too many entries")
	}
	listed := make(map[string]struct{}, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		if entry.Name == "manifest.json" || !validArchivePath(entry.Name) {
			return fmt.Errorf("invalid manifest entry name %q", entry.Name)
		}
		if _, ok := listed[entry.Name]; ok {
			return fmt.Errorf("duplicate manifest entry %q", entry.Name)
		}
		listed[entry.Name] = struct{}{}
		if entry.Size < 0 || len(entry.SHA256) != sha256.Size*2 {
			return fmt.Errorf("invalid metadata for entry %q", entry.Name)
		}
		if _, err := hex.DecodeString(entry.SHA256); err != nil {
			return fmt.Errorf("invalid checksum for entry %q", entry.Name)
		}
		actual, ok := seen[entry.Name]
		if !ok {
			return fmt.Errorf("listed entry %q is missing", entry.Name)
		}
		if actual.size != entry.Size || actual.hash != strings.ToLower(entry.SHA256) {
			return fmt.Errorf("entry %q checksum or size mismatch", entry.Name)
		}
	}
	for name := range seen {
		if _, ok := listed[name]; !ok {
			return fmt.Errorf("unlisted archive entry %q", name)
		}
	}
	for _, required := range []string{"trace.out", "metrics.json"} {
		entry, ok := seen[required]
		if !ok || entry.size == 0 {
			return fmt.Errorf("required payload %q is missing or empty", required)
		}
	}
	return nil
}

type boundedReader struct {
	reader    io.Reader
	remaining int64
}

func (r *boundedReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		var probe [1]byte
		n, err := r.reader.Read(probe[:])
		if n > 0 {
			return 0, ErrBudgetExceeded
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func consumeZeroPadding(reader io.Reader) error {
	buffer := make([]byte, 32<<10)
	for {
		n, err := reader.Read(buffer)
		for _, b := range buffer[:n] {
			if b != 0 {
				return fmt.Errorf("non-zero data follows tar end marker")
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("finish gzip stream: %w", err)
		}
	}
}

const traceHeaderSize = 16

type prefixWriter struct {
	max   int
	bytes []byte
}

func (w *prefixWriter) Write(p []byte) (int, error) {
	remaining := w.max - len(w.bytes)
	if remaining > 0 {
		w.bytes = append(w.bytes, p[:min(remaining, len(p))]...)
	}
	return len(p), nil
}

func validateTraceHeader(header []byte) error {
	if len(header) < traceHeaderSize || string(header[:5]) != "go 1." || header[5] < '0' || header[5] > '9' || header[6] < '0' || header[6] > '9' || string(header[7:13]) != " trace" || header[13] != 0 || header[14] != 0 || header[15] != 0 {
		return fmt.Errorf("trace.out has an invalid Go trace header")
	}
	return nil
}

func validateProfileName(name string) error {
	if !strings.HasSuffix(name, ".pb.gz") {
		return fmt.Errorf("unsupported profile path %q", name)
	}
	profileName := strings.TrimSuffix(strings.TrimPrefix(name, "profiles/"), ".pb.gz")
	if _, ok := allowedProfiles[profileName]; !ok {
		return fmt.Errorf("unsupported profile %q", profileName)
	}
	return nil
}

func validateProfileGzip(reader io.Reader) error {
	gz, err := gzip.NewReader(reader)
	if err != nil {
		return fmt.Errorf("open nested profile gzip: %w", err)
	}
	bounded := &boundedReader{reader: gz, remaining: maxProfileDecompressedBytes}
	_, copyErr := io.Copy(io.Discard, bounded)
	closeErr := gz.Close()
	if copyErr != nil {
		return fmt.Errorf("validate nested profile gzip: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close nested profile gzip: %w", closeErr)
	}
	return nil
}

func validateMetrics(reader io.Reader) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var document metricsDocument
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode metrics.json: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("decode metrics.json: %w", err)
	}
	if document.Schema != metricsSchema || document.Version != metricsVersion {
		return fmt.Errorf("unsupported metrics schema %q version %d", document.Schema, document.Version)
	}
	if len(document.Values) > maxMetricValues {
		return fmt.Errorf("metrics.json contains too many values")
	}
	seen := make(map[string]struct{}, len(document.Values))
	for _, value := range document.Values {
		if err := validatePortableMetricName(value.Name); err != nil {
			return err
		}
		if _, duplicate := seen[value.Name]; duplicate {
			return fmt.Errorf("duplicate runtime metric %q", value.Name)
		}
		seen[value.Name] = struct{}{}
		switch {
		case value.Kind == "uint64" && value.Uint64 != nil && value.Float64 == nil:
		case value.Kind == "float64" && value.Float64 != nil && value.Uint64 == nil:
		default:
			return fmt.Errorf("runtime metric %q has inconsistent scalar type", value.Name)
		}
	}
	return nil
}

func validatePortableMetricName(name string) error {
	if name == "" || len(name) > maxMetricNameBytes || !utf8.ValidString(name) {
		return fmt.Errorf("invalid runtime metric name %q", name)
	}
	colon := strings.IndexByte(name, ':')
	if colon <= 1 || colon != strings.LastIndexByte(name, ':') || name[0] != '/' || colon == len(name)-1 {
		return fmt.Errorf("invalid runtime metric name %q", name)
	}
	pathPart := name[:colon]
	if strings.HasSuffix(pathPart, "/") || strings.Contains(pathPart, "//") {
		return fmt.Errorf("invalid runtime metric name %q", name)
	}
	for _, r := range name {
		if r == utf8.RuneError || unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("invalid runtime metric name %q", name)
		}
	}
	return nil
}

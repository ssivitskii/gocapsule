package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ssivitskii/gocapsule/capsule"
)

func TestDemoAndVerify(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"demo", "-output", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("demo exit=%d stderr=%s", code, stderr.String())
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("archives=%v output=%s", matches, stdout.String())
	}
	if _, err := capsule.Verify(matches[0]); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"verify", matches[0]}, &stdout, &stderr); code != 0 {
		t.Fatalf("verify exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "verified") {
		t.Fatalf("verify output=%q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"inspect", matches[0]}, &stdout, &stderr); code != 0 {
		t.Fatalf("inspect exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"schema": "gocapsule.manifest"`) {
		t.Fatalf("inspect output=%q", stdout.String())
	}
}

func TestUsageErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("empty args exit=%d", code)
	}
	stderr.Reset()
	if code := run([]string{"verify"}, &stdout, &stderr); code != 1 {
		t.Fatalf("verify without path exit=%d", code)
	}
	if err := os.WriteFile(filepath.Join(t.TempDir(), "not-an-archive"), []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
}

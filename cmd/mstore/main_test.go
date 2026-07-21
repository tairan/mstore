package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chieworks/mstore/internal/providers"
	"github.com/chieworks/mstore/internal/reconcile"
)

func TestCLIImportPathAndJSON(t *testing.T) {
	cache := t.TempDir()
	repo := filepath.Join(cache, "models--Acme--Widget")
	blob := filepath.Join(repo, "blobs", "a")
	snapshot := filepath.Join(repo, "snapshots", "0123456789abcdef")
	if err := os.MkdirAll(filepath.Dir(blob), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(snapshot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blob, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../blobs/a", filepath.Join(snapshot, "model.bin")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HF_HUB_CACHE", cache)
	storeRoot := t.TempDir()
	var out, errOut bytes.Buffer
	code := run([]string{"--store", storeRoot, "import", "--activate", "hf:Acme/Widget@0123456789abcdef"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code %d, stderr %s", code, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	code = run([]string{"--store", storeRoot, "path", "widget"}, &out, &errOut)
	if code != 0 || strings.Contains(strings.TrimSpace(out.String()), "\n") {
		t.Fatalf("path output code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	out.Reset()
	code = run([]string{"--store", storeRoot, "--json", "list", "--versions"}, &out, &errOut)
	if code != 0 || !strings.HasPrefix(out.String(), "[") {
		t.Fatalf("JSON output: code=%d %q", code, out.String())
	}
}

func TestCLIUsageExitCode(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"unknown"}, &out, &errOut); code != 2 {
		t.Fatalf("code = %d", code)
	}
}

func TestExitCodeClassifiesProviderScanFailure(t *testing.T) {
	err := &reconcile.RunError{
		Failed: 1,
		Cause:  providers.ScanError{Provider: "hf", Err: errors.New("open /models: permission denied")},
	}
	if got := exitCode(err); got != 3 {
		t.Fatalf("exit code = %d", got)
	}
}

func TestCLISyncImportsAllReadyAndReportsJSON(t *testing.T) {
	cache := t.TempDir()
	repo := filepath.Join(cache, "models--Acme--Widget")
	blob := filepath.Join(repo, "blobs", "a")
	snapshot := filepath.Join(repo, "snapshots", "0123456789abcdef")
	if err := os.MkdirAll(filepath.Dir(blob), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(snapshot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blob, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../blobs/a", filepath.Join(snapshot, "model.bin")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HF_HUB_CACHE", cache)
	t.Setenv("MODELSCOPE_CACHE", filepath.Join(t.TempDir(), "missing"))
	storeRoot := filepath.Join(t.TempDir(), "store")

	var out, errOut bytes.Buffer
	code := run([]string{"--store", storeRoot, "sync", "--jobs", "2"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code %d, stdout %s, stderr %s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "imported") || !strings.Contains(out.String(), "summary:") {
		t.Fatalf("unexpected sync output: %q", out.String())
	}

	out.Reset()
	errOut.Reset()
	code = run([]string{"--store", storeRoot, "--json", "sync"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code %d, stderr %s", code, errOut.String())
	}
	var report struct {
		Summary struct {
			Skipped int `json:"skipped"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Summary.Skipped != 2 { // imported model plus missing ModelScope cache
		t.Fatalf("unexpected JSON report: %s", out.String())
	}
}

func TestCLIRemovesImportAllNewAndProvider(t *testing.T) {
	for _, args := range [][]string{
		{"import", "--all-new"},
		{"import", "--provider", "hf", "hf:Acme/Widget"},
	} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 2 {
			t.Fatalf("%v: code=%d stderr=%q", args, code, errOut.String())
		}
	}
}

func TestCLISyncUnknownModelIsUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"--store", t.TempDir(), "sync", "missing"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
}

func TestCLISyncQuietAndVerboseSkippedOutput(t *testing.T) {
	t.Setenv("HF_HUB_CACHE", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("MODELSCOPE_CACHE", filepath.Join(t.TempDir(), "missing"))
	storeRoot := filepath.Join(t.TempDir(), "store")
	var out, errOut bytes.Buffer

	code := run([]string{"--store", storeRoot, "--quiet", "sync"}, &out, &errOut)
	if code != 0 || out.Len() != 0 {
		t.Fatalf("quiet: code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}

	out.Reset()
	errOut.Reset()
	code = run([]string{"--store", storeRoot, "--verbose", "sync"}, &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), "skipped") {
		t.Fatalf("verbose: code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

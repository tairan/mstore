package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

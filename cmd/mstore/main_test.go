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
	"github.com/chieworks/mstore/internal/source"
	"github.com/chieworks/mstore/internal/store"
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

func TestCLIHelpIsAlignedAndIncludesGenerate(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--help"}, &out, &errOut); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
	for _, line := range []string{
		"  generate, gen    Generate a Bash model download script from stored manifests.",
		"  --store PATH   Store root (default: ${MSTORE_HOME:-~/models}).",
		"  mstore generate --all > download-models.sh",
	} {
		if !strings.Contains(out.String(), line) {
			t.Fatalf("help is missing %q:\n%s", line, out.String())
		}
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

func TestCLIGenerateUsesRecordedSources(t *testing.T) {
	storeRoot := t.TempDir()
	s, err := store.Open(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	importModel := func(provider, repo, revision, name string, activate bool) {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := s.Import(source.Model{
			Provider: provider, Repo: repo, Revision: revision, Path: dir, Status: "ready",
		}, store.ImportOptions{Name: name, Activate: activate})
		if err != nil {
			t.Fatal(err)
		}
	}
	oldRevision := "1111111111111111111111111111111111111111"
	currentRevision := "2222222222222222222222222222222222222222"
	msRevision := "v1.2.3"
	importModel("hf", "Acme/Widget", oldRevision, "widget", false)
	importModel("hf", "Acme/Widget", currentRevision, "widget", true)
	importModel("ms", "Qwen/Demo", msRevision, "demo", true)

	var out, errOut bytes.Buffer
	code := run([]string{"--store", storeRoot, "generate", "--all"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	got := out.String()
	for _, command := range []string{
		"hf download 'Acme/Widget' --revision '" + oldRevision + "'",
		"hf download 'Acme/Widget' --revision '" + currentRevision + "'",
		"modelscope download --model 'Qwen/Demo' --revision '" + msRevision + "'",
	} {
		if !strings.Contains(got, command) {
			t.Fatalf("generated script missing %q:\n%s", command, got)
		}
	}
	if !strings.HasPrefix(got, "#!/usr/bin/env bash\n") || !strings.Contains(got, "set -euo pipefail\n") {
		t.Fatalf("not a Bash script: %q", got)
	}

	out.Reset()
	errOut.Reset()
	code = run([]string{"--store", storeRoot, "gen", "--all"}, &out, &errOut)
	if code != 0 || out.String() != got {
		t.Fatalf("gen alias: code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}

	out.Reset()
	errOut.Reset()
	code = run([]string{"--store", storeRoot, "generate", "--all", "--current-only"}, &out, &errOut)
	if code != 0 || strings.Contains(out.String(), oldRevision) || !strings.Contains(out.String(), currentRevision) {
		t.Fatalf("current-only: code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}

	out.Reset()
	errOut.Reset()
	code = run([]string{"--store", storeRoot, "--json", "generate", "widget"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("JSON code=%d stderr=%q", code, errOut.String())
	}
	var plan struct {
		Models []struct {
			Revision string `json:"revision"`
		} `json:"models"`
		Script string `json:"script"`
	}
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Models) != 1 || plan.Models[0].Revision != currentRevision || !strings.Contains(plan.Script, currentRevision) {
		t.Fatalf("unexpected JSON plan: %s", out.String())
	}
}

func TestCLIGenerateRequiresSelection(t *testing.T) {
	for _, args := range [][]string{
		{"generate"},
		{"generate", "--all", "widget"},
		{"generate", "--current-only", "widget"},
	} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 2 {
			t.Fatalf("%v: code=%d stderr=%q", args, code, errOut.String())
		}
	}
}

func TestCLIGenerateRejectsRemovedCommandNames(t *testing.T) {
	for _, command := range []string{"download-script", "gen-download-script"} {
		var out, errOut bytes.Buffer
		if code := run([]string{command}, &out, &errOut); code != 2 {
			t.Fatalf("%s: code=%d stderr=%q", command, code, errOut.String())
		}
	}
}

func TestDownloadScriptQuotesShellArguments(t *testing.T) {
	command, err := downloadCommand("hf", "Acme/Widget'$(not-a-command)", "rev'$(not-a-command)")
	if err != nil {
		t.Fatal(err)
	}
	want := `hf download 'Acme/Widget'"'"'$(not-a-command)' --revision 'rev'"'"'$(not-a-command)'`
	if command != want {
		t.Fatalf("command=%q want=%q", command, want)
	}
}

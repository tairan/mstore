package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
		"  generate, gen        Generate a Bash download script from manifests or config.",
		"  --store PATH       Store root (default: ${MSTORE_HOME:-~/models}).",
		"  mstore config export [--output FILE] [--provider hf|ms|all] [--overwrite]",
		"      Write ./models.toml by default. Existing files are protected unless",
		"  generate:  --config FILE  --all  --current-only  --uv  --hf-mirror",
		"  mstore generate --all > download-models.sh",
		"  mstore generate --config models.toml > download-models.sh",
		"  mstore generate --uv --hf-mirror --all > download-models.sh",
	} {
		if !strings.Contains(out.String(), line) {
			t.Fatalf("help is missing %q:\n%s", line, out.String())
		}
	}
}

func TestCLIVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--version"}, &out, &errOut); code != 0 || out.String() != "mstore 0.3.0\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestCLIConfigExportDefaultsToProtectedModelsToml(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HF_HUB_CACHE", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("MODELSCOPE_CACHE", filepath.Join(t.TempDir(), "missing"))
	var out, errOut bytes.Buffer
	if code := run([]string{"config", "export"}, &out, &errOut); code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if _, err := os.Stat("models.toml"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := run([]string{"config", "export"}, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "warning: config models.toml already exists") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
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

func TestCLISyncConfigSelectsExactSource(t *testing.T) {
	cache := t.TempDir()
	for _, revision := range []string{"0123456789abcdef", "fedcba9876543210"} {
		repo := filepath.Join(cache, "models--Acme--Widget")
		blob := filepath.Join(repo, "blobs", revision)
		snapshot := filepath.Join(repo, "snapshots", revision)
		if err := os.MkdirAll(filepath.Dir(blob), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(snapshot, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(blob, []byte(revision), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../../blobs/"+revision, filepath.Join(snapshot, "model.bin")); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HF_HUB_CACHE", cache)
	t.Setenv("MODELSCOPE_CACHE", filepath.Join(t.TempDir(), "missing"))
	config := filepath.Join(t.TempDir(), "models.toml")
	if err := os.WriteFile(config, []byte("schema = 1\n\n[[models]]\nsource = \"hf:Acme/Widget@fedcba9876543210\"\nenabled = true\nname = \"widget-q4\"\n\n[[models]]\nsource = \"hf:Acme/Widget@0123456789abcdef\"\nenabled = false\nname = \"widget-q8\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	storeRoot := filepath.Join(t.TempDir(), "store")
	var out, errOut bytes.Buffer
	code := run([]string{"--store", storeRoot, "sync", "--config", config}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	s, err := store.Open(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := s.List("")
	if err != nil || len(versions) != 1 || versions[0].Name != "widget-q4" || versions[0].Manifest.Source.Revision != "fedcba9876543210" {
		t.Fatalf("versions=%#v err=%v", versions, err)
	}
}

func TestCLISyncConfigFailsForMissingSelectedSource(t *testing.T) {
	t.Setenv("HF_HUB_CACHE", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("MODELSCOPE_CACHE", filepath.Join(t.TempDir(), "missing"))
	config := filepath.Join(t.TempDir(), "models.toml")
	if err := os.WriteFile(config, []byte("schema = 1\n\n[[models]]\nsource = \"hf:Acme/Widget@0123456789abcdef\"\nenabled = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := run([]string{"--store", t.TempDir(), "sync", "--config", config}, &out, &errOut)
	if code == 0 || !strings.Contains(out.String(), "not ready") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestCLISyncConfigWithNoEnabledModelsDoesNotFallBackToFullSync(t *testing.T) {
	cache := t.TempDir()
	repo := filepath.Join(cache, "models--Acme--Widget")
	if err := os.MkdirAll(filepath.Join(repo, "blobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "snapshots", "0123456789abcdef"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "blobs", "a"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../blobs/a", filepath.Join(repo, "snapshots", "0123456789abcdef", "model.bin")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HF_HUB_CACHE", cache)
	t.Setenv("MODELSCOPE_CACHE", filepath.Join(t.TempDir(), "missing"))
	config := filepath.Join(t.TempDir(), "models.toml")
	if err := os.WriteFile(config, []byte("schema = 1\n\n[[models]]\nsource = \"hf:Acme/Widget@0123456789abcdef\"\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	storeRoot := filepath.Join(t.TempDir(), "store")
	var out, errOut bytes.Buffer
	if code := run([]string{"--store", storeRoot, "sync", "--config", config}, &out, &errOut); code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	s, err := store.Open(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := s.List("")
	if err != nil || len(versions) != 0 {
		t.Fatalf("versions=%#v err=%v", versions, err)
	}
}

func TestCLISyncAndGenerateUseCanonicalModelScopeRepo(t *testing.T) {
	cache := t.TempDir()
	dir := filepath.Join(cache, "models", "Qwen", "Demo-0___6B")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mv"), []byte("master"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HF_HUB_CACHE", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("MODELSCOPE_CACHE", cache)
	storeRoot := filepath.Join(t.TempDir(), "store")

	var out, errOut bytes.Buffer
	code := run([]string{"--store", storeRoot, "sync", "--provider", "ms"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("sync code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}

	s, err := store.Open(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := s.List("")
	if err != nil || len(versions) != 1 || versions[0].Manifest.Source.Repo != "Qwen/Demo-0.6B" {
		t.Fatalf("synced versions: %#v, %v", versions, err)
	}

	out.Reset()
	errOut.Reset()
	code = run([]string{"--store", storeRoot, "generate", "--all"}, &out, &errOut)
	want := "modelscope download --model 'Qwen/Demo-0.6B' --revision 'master'"
	if code != 0 || !strings.Contains(out.String(), want) {
		t.Fatalf("generate code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
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
	for _, activation := range []string{
		"mstore --store \"$MSTORE_STORE\" import --name 'widget' --version '" + currentRevision[:12] + "' --activate 'hf:Acme/Widget@" + currentRevision + "'",
		"mstore --store \"$MSTORE_STORE\" import --name 'demo' --version '" + msRevision + "' --activate 'ms:Qwen/Demo@" + msRevision + "'",
	} {
		if !strings.Contains(got, activation) {
			t.Fatalf("generated script missing activation %q:\n%s", activation, got)
		}
	}
	if !strings.HasPrefix(got, "#!/usr/bin/env bash\n") || !strings.Contains(got, "set -euo pipefail\n") {
		t.Fatalf("not a Bash script: %q", got)
	}
	if strings.Contains(got, "mstore --store \"$MSTORE_STORE\" sync") {
		t.Fatalf("generated script must not perform an unfiltered sync: %q", got)
	}
	for _, line := range []string{
		"MSTORE_DOWNLOAD_CACHE=\"$(mktemp -d)\"",
		"MSTORE_SOURCE_CACHE=\"$MSTORE_DOWNLOAD_CACHE/source-0\"",
		"export HF_HUB_CACHE=\"$MSTORE_SOURCE_CACHE/huggingface\"",
		"export MODELSCOPE_CACHE=\"$MSTORE_SOURCE_CACHE/modelscope\"",
		"trap 'rm -rf -- \"$MSTORE_DOWNLOAD_CACHE\"' EXIT",
	} {
		if !strings.Contains(got, line) {
			t.Fatalf("generated script missing cache isolation %q: %s", line, got)
		}
	}

	out.Reset()
	errOut.Reset()
	code = run([]string{"--store", storeRoot, "generate", "--uv", "--all"}, &out, &errOut)
	uvScript := out.String()
	for _, command := range []string{
		"uvx --from huggingface_hub hf download 'Acme/Widget' --revision '" + oldRevision + "'",
		"uvx modelscope download --model 'Qwen/Demo' --revision '" + msRevision + "'",
	} {
		if code != 0 || !strings.Contains(uvScript, command) {
			t.Fatalf("uv: code=%d stdout=%q stderr=%q", code, uvScript, errOut.String())
		}
	}
	if !strings.Contains(uvScript, "# This script uses uvx; install uv before running it.\n") {
		t.Fatalf("uv script is missing its prerequisite note: %q", uvScript)
	}

	out.Reset()
	errOut.Reset()
	code = run([]string{"--store", storeRoot, "generate", "--hf-mirror", "--all"}, &out, &errOut)
	mirrorScript := out.String()
	if code != 0 ||
		!strings.Contains(mirrorScript, "HF_ENDPOINT='https://hf-mirror.com' hf download 'Acme/Widget' --revision '"+oldRevision+"'") ||
		!strings.Contains(mirrorScript, "modelscope download --model 'Qwen/Demo' --revision '"+msRevision+"'") ||
		strings.Contains(mirrorScript, "HF_ENDPOINT='https://hf-mirror.com' modelscope") {
		t.Fatalf("hf-mirror: code=%d stdout=%q stderr=%q", code, mirrorScript, errOut.String())
	}

	out.Reset()
	errOut.Reset()
	code = run([]string{"--store", storeRoot, "generate", "--uv", "--hf-mirror", "--all"}, &out, &errOut)
	combinedScript := out.String()
	if code != 0 ||
		!strings.Contains(combinedScript, "HF_ENDPOINT='https://hf-mirror.com' uvx --from huggingface_hub hf download 'Acme/Widget' --revision '"+oldRevision+"'") ||
		!strings.Contains(combinedScript, "uvx modelscope download --model 'Qwen/Demo' --revision '"+msRevision+"'") {
		t.Fatalf("uv with hf-mirror: code=%d stdout=%q stderr=%q", code, combinedScript, errOut.String())
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
	code = run([]string{"--store", storeRoot, "--json", "generate", "--uv", "--hf-mirror", "widget"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("JSON code=%d stderr=%q", code, errOut.String())
	}
	var plan struct {
		Models []struct {
			Revision string `json:"revision"`
			Command  string `json:"command"`
			Current  bool   `json:"current"`
		} `json:"models"`
		Script string `json:"script"`
	}
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	wantCommand := "HF_ENDPOINT='https://hf-mirror.com' uvx --from huggingface_hub hf download 'Acme/Widget' --revision '" + currentRevision + "'"
	if len(plan.Models) != 1 || plan.Models[0].Revision != currentRevision || !plan.Models[0].Current || plan.Models[0].Command != wantCommand || !strings.Contains(plan.Script, wantCommand) {
		t.Fatalf("unexpected JSON plan: %s", out.String())
	}
}

func TestCLIGenerateFromConfig(t *testing.T) {
	config := filepath.Join(t.TempDir(), "models.toml")
	hfRevision := "1111111111111111111111111111111111111111"
	msRevision := "v1.2.3"
	contents := "schema = 1\n\n[defaults]\nhash = true\n\n" +
		"[[models]]\nsource = \"hf:Acme/Widget@" + hfRevision + "\"\nenabled = true\nname = \"widget\"\n\n" +
		"[[models]]\nsource = \"ms:Qwen/Demo@" + msRevision + "\"\nenabled = true\nname = \"demo\"\n\n" +
		"[[models]]\nsource = \"hf:Acme/Disabled@2222222222222222222222222222222222222222\"\nenabled = false\nname = \"disabled\"\n"
	if err := os.WriteFile(config, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	code := run([]string{"generate", "--config", config}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	script := out.String()
	for _, want := range []string{
		"hf download 'Acme/Widget' --revision '" + hfRevision + "'",
		"modelscope download --model 'Qwen/Demo' --revision '" + msRevision + "'",
		"mstore --store \"$MSTORE_STORE\" import --name 'widget' --hash 'hf:Acme/Widget@" + hfRevision + "'",
		"mstore --store \"$MSTORE_STORE\" import --name 'demo' --hash 'ms:Qwen/Demo@" + msRevision + "'",
		"# WARNING: ModelScope revision Qwen/Demo@v1.2.3 is not an immutable commit ID; it may move before the script runs.",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("generated script missing %q:\n%s", want, script)
		}
	}
	for _, unwanted := range []string{"Disabled", " --activate ", " --version "} {
		if strings.Contains(script, unwanted) {
			t.Fatalf("generated script unexpectedly contains %q:\n%s", unwanted, script)
		}
	}

	out.Reset()
	errOut.Reset()
	code = run([]string{"generate", "--uv", "--hf-mirror", "--config", config}, &out, &errOut)
	if code != 0 ||
		!strings.Contains(out.String(), "HF_ENDPOINT='https://hf-mirror.com' uvx --from huggingface_hub hf download 'Acme/Widget' --revision '"+hfRevision+"'") ||
		!strings.Contains(out.String(), "uvx modelscope download --model 'Qwen/Demo' --revision '"+msRevision+"'") {
		t.Fatalf("uv with hf-mirror: code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}

	out.Reset()
	errOut.Reset()
	code = run([]string{"--json", "generate", "--config", config}, &out, &errOut)
	if code != 0 {
		t.Fatalf("JSON code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var plan downloadScriptPlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Models) != 2 || plan.Models[0].Name != "widget" || plan.Models[0].Version != "" || plan.Models[0].Current {
		t.Fatalf("unexpected JSON plan: %s", out.String())
	}

	emptyConfig := filepath.Join(t.TempDir(), "empty.toml")
	if err := os.WriteFile(emptyConfig, []byte("schema = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	code = run([]string{"generate", "--config", emptyConfig}, &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), "set -euo pipefail\n") || strings.Contains(out.String(), "MSTORE_DOWNLOAD_CACHE") {
		t.Fatalf("empty config: code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestCLIGenerateFromConfigRejectsConflictingSelectors(t *testing.T) {
	config := filepath.Join(t.TempDir(), "models.toml")
	if err := os.WriteFile(config, []byte("schema = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"generate", "--config", config, "--all"},
		{"generate", "--config", config, "widget"},
		{"generate", "--config", config, "--current-only"},
	} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 2 {
			t.Fatalf("args=%q code=%d stdout=%q stderr=%q", args, code, out.String(), errOut.String())
		}
	}

	var out, errOut bytes.Buffer
	if code := run([]string{"generate", "--config", filepath.Join(t.TempDir(), "missing.toml")}, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "read config") {
		t.Fatalf("missing config: code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestCLIGenerateRejectsDifferentAliasInventories(t *testing.T) {
	storeRoot := t.TempDir()
	s, err := store.Open(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	revision := "1111111111111111111111111111111111111111"
	version := ""
	for i, name := range []string{"widget-a", "widget-b"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			if err := os.WriteFile(filepath.Join(dir, "weights.bin"), []byte("weights"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		src := source.Model{Provider: "hf", Repo: "Acme/Widget", Revision: revision, Path: dir, Status: "ready"}
		result, err := s.Import(src, store.ImportOptions{Name: name, Hash: true})
		if err != nil {
			t.Fatal(err)
		}
		if version == "" {
			version = result.Version
		}
	}

	var out, errOut bytes.Buffer
	code := run([]string{"--store", storeRoot, "--json", "generate", "widget-a@" + version, "widget-b@" + version}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "different stored content") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestCLIGenerateJSONRetainsSelectedAliases(t *testing.T) {
	storeRoot := t.TempDir()
	s, err := store.Open(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	revision := "1111111111111111111111111111111111111111"
	version := ""
	for _, name := range []string{"widget-a", "widget-b"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		result, err := s.Import(source.Model{Provider: "hf", Repo: "Acme/Widget", Revision: revision, Path: dir, Status: "ready"}, store.ImportOptions{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		if version == "" {
			version = result.Version
		}
	}

	var out, errOut bytes.Buffer
	code := run([]string{"--store", storeRoot, "--json", "generate", "widget-a@" + version, "widget-b@" + version}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var plan downloadScriptPlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Models) != 2 || strings.Count(plan.Script, "hf download 'Acme/Widget' --revision '"+revision+"'") != 1 {
		t.Fatalf("models=%#v script=%q", plan.Models, plan.Script)
	}
}

func TestCLIGenerateRejectsAliasHashMismatch(t *testing.T) {
	storeRoot := t.TempDir()
	s, err := store.Open(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	revision := "1111111111111111111111111111111111111111"
	version := ""
	for _, name := range []string{"widget-a", "widget-b"} {
		dir := t.TempDir()
		content := []byte("a")
		if name == "widget-b" {
			content = []byte("b")
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), content, 0o644); err != nil {
			t.Fatal(err)
		}
		result, err := s.Import(source.Model{Provider: "hf", Repo: "Acme/Widget", Revision: revision, Path: dir, Status: "ready"}, store.ImportOptions{Name: name, Hash: true})
		if err != nil {
			t.Fatal(err)
		}
		if version == "" {
			version = result.Version
		}
	}

	var out, errOut bytes.Buffer
	code := run([]string{"--store", storeRoot, "generate", "widget-a@" + version, "widget-b@" + version}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "different stored content") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestCLIGenerateRejectsAliasContentMismatchWithoutHashes(t *testing.T) {
	storeRoot := t.TempDir()
	s, err := store.Open(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	revision := "1111111111111111111111111111111111111111"
	version := ""
	for _, name := range []string{"widget-a", "widget-b"} {
		dir := t.TempDir()
		content := []byte("a")
		if name == "widget-b" {
			content = []byte("b")
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), content, 0o644); err != nil {
			t.Fatal(err)
		}
		result, err := s.Import(source.Model{Provider: "hf", Repo: "Acme/Widget", Revision: revision, Path: dir, Status: "ready"}, store.ImportOptions{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		if version == "" {
			version = result.Version
		}
	}

	var out, errOut bytes.Buffer
	code := run([]string{"--store", storeRoot, "generate", "widget-a@" + version, "widget-b@" + version}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "different stored content") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestCLIGeneratePreservesMultipleModelScopeRevisions(t *testing.T) {
	storeRoot := t.TempDir()
	s, err := store.Open(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, revision := range []string{"master", "release"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(revision), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := s.Import(source.Model{
			Provider: "ms", Repo: "Qwen/Demo", Revision: revision,
			Path: dir, Status: "ready",
		}, store.ImportOptions{Name: "demo"})
		if err != nil {
			t.Fatal(err)
		}
	}

	var out, errOut bytes.Buffer
	code := run([]string{"--store", storeRoot, "generate", "--all"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	script := out.String()
	first := "modelscope download --model 'Qwen/Demo' --revision 'master'"
	second := "modelscope download --model 'Qwen/Demo' --revision 'release'"
	if !strings.Contains(script, first) || !strings.Contains(script, second) ||
		strings.Count(script, "mstore --store \"$MSTORE_STORE\" import --name 'demo'") != 2 ||
		!strings.Contains(script, "WARNING: ModelScope revision Qwen/Demo@master") {
		t.Fatalf("unexpected ModelScope script: %q", script)
	}
	firstImport := "mstore --store \"$MSTORE_STORE\" import --name 'demo' --version 'master' 'ms:Qwen/Demo@master'"
	if strings.Index(script, first) > strings.Index(script, firstImport) ||
		strings.Index(script, firstImport) > strings.Index(script, second) {
		t.Fatalf("first revision was not imported before the second: %q", script)
	}
}

func TestCLIGenerateRejectsChangedManifestInventory(t *testing.T) {
	storeRoot := t.TempDir()
	s, err := store.Open(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "weights.bin"), []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := s.Import(source.Model{
		Provider: "hf", Repo: "Acme/Widget", Revision: "111111111111aaaa",
		Path: dir, Status: "ready",
	}, store.ImportOptions{Name: "widget", Hash: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(result.Path, "config.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(result.Path, "replacement.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	code := run([]string{"--store", storeRoot, "generate", "widget@" + result.Version}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "stored file inventory differs from manifest") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestCLIGenerateRejectsChangedManifestBytes(t *testing.T) {
	storeRoot := t.TempDir()
	s, err := store.Open(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := s.Import(source.Model{
		Provider: "hf", Repo: "Acme/Widget", Revision: "111111111111aaaa",
		Path: dir, Status: "ready",
	}, store.ImportOptions{Name: "widget"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(result.Path, "config.json"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	code := run([]string{"--store", storeRoot, "generate", "widget@" + result.Version}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "stored byte count changed") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestCLIGeneratePreservesHashedImport(t *testing.T) {
	storeRoot := t.TempDir()
	s, err := store.Open(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := s.Import(source.Model{Provider: "hf", Repo: "Acme/Widget", Revision: "111111111111aaaa", Path: dir, Status: "ready"}, store.ImportOptions{Name: "widget", Hash: true})
	if err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	code := run([]string{"--store", storeRoot, "generate", "widget@" + result.Version}, &out, &errOut)
	want := "mstore --store \"$MSTORE_STORE\" import --name 'widget' --version '" + result.Version + "' --hash 'hf:Acme/Widget@111111111111aaaa'"
	if code != 0 || !strings.Contains(out.String(), want) {
		t.Fatalf("code=%d stdout=%q stderr=%q want=%q", code, out.String(), errOut.String(), want)
	}
}

func TestDownloadCommandsChunkLargeInventories(t *testing.T) {
	files := make([]string, 0, 2000)
	for i := range 2000 {
		files = append(files, fmt.Sprintf("weights/%04d-%s.bin", i, strings.Repeat("x", 60)))
	}
	commands, err := downloadCommands("hf", "Acme/Widget", "111111111111aaaa", files, downloadScriptOptions{})
	if err != nil || len(commands) < 2 {
		t.Fatalf("commands=%d err=%v", len(commands), err)
	}
	for _, command := range commands {
		if len(command) > maxDownloadCommandLength {
			t.Fatalf("command length=%d exceeds limit", len(command))
		}
	}
	modelScopeCommands, err := downloadCommands("ms", "Acme/Widget", "111111111111aaaa", files, downloadScriptOptions{})
	if err != nil || len(modelScopeCommands) < 2 {
		t.Fatalf("ModelScope commands=%d err=%v", len(modelScopeCommands), err)
	}
	for _, command := range modelScopeCommands {
		if len(command) > maxDownloadCommandLength {
			t.Fatalf("ModelScope command length=%d exceeds limit", len(command))
		}
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
	command, err := downloadCommand("hf", "Acme/Widget'$(not-a-command)", "rev'$(not-a-command)", nil, downloadScriptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := `hf download 'Acme/Widget'"'"'$(not-a-command)' --revision 'rev'"'"'$(not-a-command)'`
	if command != want {
		t.Fatalf("command=%q want=%q", command, want)
	}
	command, err = downloadCommand("hf", "Acme/Widget", "rev", []string{"-not-an-option"}, downloadScriptOptions{})
	if err != nil || !strings.Contains(command, " -- '-not-an-option'") {
		t.Fatalf("file option delimiter missing: %q err=%v", command, err)
	}
}

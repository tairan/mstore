package modelscope

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chieworks/mstore/internal/source"
)

func TestScanCurrentLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "models")
	dir := filepath.Join(root, "BAAI--bge-m3", "snapshots", "master")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	models, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Repo != "BAAI/bge-m3" ||
		models[0].Path != dir || models[0].Revision != "master" ||
		models[0].Status != "ready" || !models[0].Preferred {
		t.Fatalf("unexpected models: %#v", models)
	}
}

func TestScanListsMultipleSnapshotsIndependently(t *testing.T) {
	cache := t.TempDir()
	root := filepath.Join(cache, "models")
	for _, revision := range []string{"main", "v1.2.3"} {
		dir := filepath.Join(root, "Qwen--Demo", "snapshots", revision)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MODELSCOPE_CACHE", cache)

	model, err := Resolve(source.Ref{Provider: "ms", Repo: "Qwen/Demo", Revision: "v1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	if model.Repo != "Qwen/Demo" || model.Revision != "v1.2.3" ||
		model.Path != filepath.Join(root, "Qwen--Demo", "snapshots", "v1.2.3") {
		t.Fatalf("unexpected resolved model: %#v", model)
	}
}

func TestResolveRejectsAmbiguousSnapshotSelections(t *testing.T) {
	cache := t.TempDir()
	root := filepath.Join(cache, "models")
	for _, revision := range []string{"v1", "v2"} {
		dir := filepath.Join(root, "Qwen--Demo", "snapshots", revision)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MODELSCOPE_CACHE", cache)

	for _, ref := range []source.Ref{
		{Provider: "ms", Repo: "Qwen/Demo"},
		{Provider: "ms", Repo: "Qwen/Demo", Revision: "v"},
	} {
		_, err := Resolve(ref)
		if err == nil || !strings.Contains(err.Error(), "multiple revisions") && !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("Resolve(%#v) error = %v", ref, err)
		}
	}
}

func TestResolvePrefersExactSnapshotRevision(t *testing.T) {
	cache := t.TempDir()
	root := filepath.Join(cache, "models")
	for _, revision := range []string{"v1", "v10"} {
		dir := filepath.Join(root, "Qwen--Demo", "snapshots", revision)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MODELSCOPE_CACHE", cache)

	model, err := Resolve(source.Ref{Provider: "ms", Repo: "Qwen/Demo", Revision: "v1"})
	if err != nil || model.Revision != "v1" {
		t.Fatalf("Resolve exact revision = %#v, %v", model, err)
	}
	if err := os.Remove(filepath.Join(root, "Qwen--Demo", "snapshots", "v1", "config.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(source.Ref{Provider: "ms", Repo: "Qwen/Demo", Revision: "v1"}); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("Resolve invalid exact revision error = %v", err)
	}
}

func TestScanMarksInvalidSnapshotsNotReady(t *testing.T) {
	root := filepath.Join(t.TempDir(), "models")
	dir := filepath.Join(root, "BAAI--bge-m3", "snapshots", "master")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	models, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Status != "invalid" || models[0].Error == "" {
		t.Fatalf("unexpected models: %#v", models)
	}
}

func TestScanReportsUnreadableSnapshotsDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "models")
	if err := os.MkdirAll(filepath.Join(root, "BAAI--bge-m3"), 0o755); err != nil {
		t.Fatal(err)
	}
	models, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Status != "incomplete" || !strings.Contains(models[0].Error, "read snapshots") {
		t.Fatalf("unexpected models: %#v", models)
	}
}

func TestScanIgnoresLegacyLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "models")
	legacy := filepath.Join(root, "BAAI", "bge-m3")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, ".mv"), []byte("master"), 0o644); err != nil {
		t.Fatal(err)
	}
	models, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 0 {
		t.Fatalf("unexpected models: %#v", models)
	}
}

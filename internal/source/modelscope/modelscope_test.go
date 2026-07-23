package modelscope

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/chieworks/mstore/internal/source"
)

func TestScanCurrentLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "models")
	dir := filepath.Join(root, "Qwen", "Talker-0___6B")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mv"), []byte("Revision:abc123def456,CreatedAt:today"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	models, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Repo != "Qwen/Talker-0.6B" ||
		models[0].Path != dir || models[0].Revision != "abc123def456" ||
		models[0].Status != "ready" || !models[0].Preferred {
		t.Fatalf("unexpected models: %#v", models)
	}
}

func TestResolveUsesCanonicalRepoForMaskedCacheDirectory(t *testing.T) {
	cache := t.TempDir()
	root := filepath.Join(cache, "models")
	dir := filepath.Join(root, "Qwen", "Talker-0___6B")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mv"), []byte("master"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MODELSCOPE_CACHE", cache)

	model, err := Resolve(source.Ref{Provider: "ms", Repo: "Qwen/Talker-0.6B", Revision: "master"})
	if err != nil {
		t.Fatal(err)
	}
	if model.Repo != "Qwen/Talker-0.6B" || model.Path != dir {
		t.Fatalf("unexpected resolved model: %#v", model)
	}
}

func TestScanRejectsOldRootAndMissingMV(t *testing.T) {
	old := filepath.Join(t.TempDir(), "hub")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(old); err == nil {
		t.Fatal("expected unsupported layout")
	}
	root := filepath.Join(t.TempDir(), "models")
	if err := os.MkdirAll(filepath.Join(root, "owner", "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	models, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Status != "incomplete" {
		t.Fatalf("unexpected models: %#v", models)
	}
}

func TestScanMissingModelsDirectoryPreservesNotExist(t *testing.T) {
	base := t.TempDir()
	_, err := Scan(filepath.Join(base, "models"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected not-exist error, got %v", err)
	}
}

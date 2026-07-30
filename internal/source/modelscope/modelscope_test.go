package modelscope

import (
	"os"
	"path/filepath"
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

package modelscope

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanCurrentLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "models")
	dir := filepath.Join(root, "Qwen", "Talker")
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
	if len(models) != 1 || models[0].Revision != "abc123def456" ||
		models[0].Status != "ready" || !models[0].Preferred {
		t.Fatalf("unexpected models: %#v", models)
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

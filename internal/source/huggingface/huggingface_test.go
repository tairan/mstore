package huggingface

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chieworks/mstore/internal/source"
)

func TestScanSnapshotAndDanglingSymlink(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "models--Acme--Model")
	blob := filepath.Join(repo, "blobs", "abc")
	if err := os.MkdirAll(filepath.Dir(blob), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blob, []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(repo, "snapshots", "0123456789abcdef")
	if err := os.MkdirAll(snapshot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../blobs/abc", filepath.Join(snapshot, "model.bin")); err != nil {
		t.Fatal(err)
	}
	models, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Repo != "Acme/Model" || models[0].Status != "ready" {
		t.Fatalf("unexpected models: %#v", models)
	}

	if err := os.Remove(blob); err != nil {
		t.Fatal(err)
	}
	models, err = Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if models[0].Status != "invalid" {
		t.Fatalf("dangling symlink status = %q", models[0].Status)
	}
}

func TestScanRejectsSymlinkOutsideBlobs(t *testing.T) {
	root := t.TempDir()
	snapshot := filepath.Join(root, "models--Acme--Model", "snapshots", "abcdef")
	if err := os.MkdirAll(snapshot, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(snapshot, "model.bin")); err != nil {
		t.Fatal(err)
	}
	models, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Status != "invalid" {
		t.Fatalf("unexpected models: %#v", models)
	}
}

func TestResolveNamedRef(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HF_HUB_CACHE", root)
	repo := filepath.Join(root, "models--Acme--Model")
	snapshot := filepath.Join(repo, "snapshots", "abcdef1234567890")
	if err := os.MkdirAll(snapshot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshot, "model.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "refs", "main"), []byte("abcdef1234567890\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Resolve(source.Ref{Provider: "hf", Repo: "Acme/Model", Revision: "main"})
	if err != nil || m.Revision != "abcdef1234567890" {
		t.Fatalf("resolve ref: %#v %v", m, err)
	}
}

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

	if err := os.RemoveAll(filepath.Join(root, "models--Acme--Model", "snapshots")); err != nil {
		t.Fatal(err)
	}
	models, err = Scan(root)
	if err != nil || len(models) != 1 || models[0].Status != "incomplete" || models[0].Path != filepath.Join(root, "models--Acme--Model") {
		t.Fatalf("incomplete model target: %#v, %v", models, err)
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

func TestScanReportsEmptySnapshotsAsIncomplete(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "models--Acme--Empty")
	if err := os.MkdirAll(filepath.Join(repo, "snapshots"), 0o755); err != nil {
		t.Fatal(err)
	}
	models, err := Scan(root)
	if err != nil || len(models) != 1 || models[0].Status != "incomplete" || models[0].Path != repo {
		t.Fatalf("models=%#v err=%v", models, err)
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
	models, err := Scan(root)
	if err != nil || len(models) != 1 || !models[0].Preferred {
		t.Fatalf("preferred revision: %#v %v", models, err)
	}
}

func TestPreferredRevisionFallsBackToMaster(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "refs", "master"), []byte("master-revision\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := preferredRevision(repo); got != "master-revision" {
		t.Fatalf("preferred revision = %q", got)
	}
}

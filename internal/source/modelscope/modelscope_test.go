package modelscope

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chieworks/mstore/internal/source"
)

func makeRepo(t *testing.T, root, namespace, encoded, marker string, files bool) string {
	t.Helper()
	dir := filepath.Join(root, namespace, encoded)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if marker != "" {
		if err := os.WriteFile(filepath.Join(dir, ".mv"), []byte(marker), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if files {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCacheRoot(t *testing.T) {
	t.Setenv("MODELSCOPE_CACHE", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := CacheRoot()
	if err != nil {
		t.Fatal(err)
	}
	if root != filepath.Join(home, ".cache", "modelscope", "hub", "models") {
		t.Fatalf("root=%q", root)
	}
	cache := filepath.Join(t.TempDir(), "cache")
	t.Setenv("MODELSCOPE_CACHE", cache)
	root, err = CacheRoot()
	if err != nil || root != filepath.Join(cache, "models") {
		t.Fatalf("root=%q err=%v", root, err)
	}
}

func TestScanNewLayoutAndMVFormats(t *testing.T) {
	root := t.TempDir()
	plain := makeRepo(t, root, "Qwen", "Demo___0___6B", "master", true)
	makeRepo(t, root, "Qwen", "Other", "Revision:v1.2.3,CreatedAt:2026-07-31T00:00:00Z", true)
	makeRepo(t, root, "Qwen", "snapshots", "release", true)
	models, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 {
		t.Fatalf("models=%#v", models)
	}
	for _, m := range models {
		if m.Status != "ready" {
			t.Fatalf("model=%#v", m)
		}
		if m.Repo == "Qwen/Demo.0.6B" && m.Path != plain {
			t.Fatalf("path=%q", m.Path)
		}
		if m.Repo == "Qwen/Other" && m.Revision != "v1.2.3" {
			t.Fatalf("model=%#v", m)
		}
	}
}

func TestScanStatesAndIgnoresLegacyLayout(t *testing.T) {
	root := t.TempDir()
	makeRepo(t, root, "Acme", "Missing", "", true)
	makeRepo(t, root, "Acme", "Empty", "v1", false)
	makeRepo(t, root, "Acme", "Bad", "Revision:../escape,CreatedAt:now", true)
	currentSnapshots := filepath.Join(root, "Acme--Team", "snapshots", "files")
	if err := os.MkdirAll(currentSnapshots, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(currentSnapshots, "partial.tmp"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	makeRepo(t, root, "Acme--Team", "Sibling", "v1", true)
	legacy := filepath.Join(root, "Acme--Legacy", "snapshots", "master")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Acme--Legacy", ".lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	models, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 5 {
		t.Fatalf("models=%#v", models)
	}
	statuses := map[string]string{}
	for _, m := range models {
		statuses[m.Repo] = m.Status
	}
	if statuses["Acme/Missing"] != "incomplete" || statuses["Acme/Empty"] != "invalid" || statuses["Acme/Bad"] != "invalid" {
		t.Fatalf("statuses=%#v", statuses)
	}
	if statuses["Acme--Team/snapshots"] != "incomplete" || statuses["Acme--Team/Sibling"] != "ready" {
		t.Fatalf("current namespace statuses=%#v", statuses)
	}
}

func TestScanIgnoresHiddenCacheDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".cache", "download"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "Acme", ".staging"), 0o755); err != nil {
		t.Fatal(err)
	}
	models, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 0 {
		t.Fatalf("models=%#v", models)
	}
}

func TestScanRejectsTemporaryAndDanglingFiles(t *testing.T) {
	root := t.TempDir()
	dir := makeRepo(t, root, "Acme", "Broken", "v1", true)
	if err := os.WriteFile(filepath.Join(dir, "part.tmp"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	models, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Status != "invalid" || !models[0].Preferred {
		t.Fatalf("models=%#v", models)
	}
	if err := os.Remove(filepath.Join(dir, "part.tmp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(dir, "model.bin")); err != nil {
		t.Fatal(err)
	}
	models, err = Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Status != "invalid" {
		t.Fatalf("models=%#v", models)
	}
}

func TestScanRejectsReservedRevisions(t *testing.T) {
	root := t.TempDir()
	for name, revision := range map[string]string{"Current": "current", "At": "release@v1", "Hidden": ".release"} {
		makeRepo(t, root, "Acme", name, revision, true)
	}
	models, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range models {
		if model.Status != "invalid" {
			t.Fatalf("model=%#v", model)
		}
	}
}

func TestResolveExactAndPrefixRevision(t *testing.T) {
	cache := t.TempDir()
	root := filepath.Join(cache, "models")
	makeRepo(t, root, "Qwen", "Demo", "v1", true)
	// A repository directory represents one cached revision; an exact value and
	// a unique prefix must both resolve through the canonical source identity.
	t.Setenv("MODELSCOPE_CACHE", cache)
	model, err := Resolve(source.Ref{Provider: "ms", Repo: "Qwen/Demo", Revision: "v1"})
	if err != nil || model.Revision != "v1" {
		t.Fatalf("model=%#v err=%v", model, err)
	}
	if _, err := Resolve(source.Ref{Provider: "ms", Repo: "Qwen/Demo", Revision: "v"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(source.Ref{Provider: "ms", Repo: "Qwen/Demo", Revision: "v1/"}); err == nil || !strings.Contains(err.Error(), "invalid ModelScope revision") {
		t.Fatalf("err=%v", err)
	}
}

func TestRepoPathEncoding(t *testing.T) {
	root := t.TempDir()
	path, err := RepoPath(root, "Org.Name/Demo.0.6B")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "Org___Name", "Demo___0___6B"); path != want {
		t.Fatalf("path=%q want=%q", path, want)
	}
	if _, err := RepoPath(root, "Qwen/../escape"); err == nil {
		t.Fatal("unsafe repo accepted")
	}
}

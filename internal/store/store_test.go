package store

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chieworks/mstore/internal/manifest"
	"github.com/chieworks/mstore/internal/source"
)

func fixtureSource(t *testing.T, repo, revision string) source.Model {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "model.bin"), []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	return source.Model{Provider: "hf", Repo: repo, Revision: revision, Path: dir, Status: "ready"}
}

func TestImportIsIdempotentAndDereferences(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := fixtureSource(t, "Acme/Fancy_Model", "0123456789abcdef0123456789abcdef")
	target := filepath.Join(src.Path, "linked.bin")
	if err := os.Symlink("nested/model.bin", target); err != nil {
		t.Fatal(err)
	}
	first, err := s.Import(src, ImportOptions{Activate: true, Hash: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Import(src, ImportOptions{Activate: true, Hash: true})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Skipped || first.Path != second.Path {
		t.Fatalf("not idempotent: %#v %#v", first, second)
	}
	info, err := os.Lstat(filepath.Join(first.Path, "linked.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("published provider symlink was not dereferenced")
	}
	got, err := s.Resolve("fancy-model")
	if err != nil || got.Path != first.Path || !got.Current {
		t.Fatalf("resolve: %#v, %v", got, err)
	}
	got, err = s.Resolve("fancy-model@" + first.Version)
	if err != nil || !got.Current {
		t.Fatalf("explicit current resolve: %#v, %v", got, err)
	}
	if _, err := s.Verify("fancy-model", true, false); err != nil {
		t.Fatal(err)
	}
}

func TestHashImportUpgradesExistingManifest(t *testing.T) {
	s, _ := Open(t.TempDir())
	src := fixtureSource(t, "Acme/HashUpgrade", "0123456789abcdef0123456789abcdef")
	first, err := s.Import(src, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Import(src, ImportOptions{Hash: true})
	if err != nil || !second.Skipped || second.Path != first.Path {
		t.Fatalf("upgrade import: %#v %v", second, err)
	}
	m, err := manifest.Read(first.Path)
	if err != nil || !manifestHasCompleteHashes(m) {
		t.Fatalf("manifest hashes not upgraded: %#v %v", m, err)
	}
}

func TestVerifyDetectsTamperAndCopyIsIdempotent(t *testing.T) {
	srcStore, _ := Open(t.TempDir())
	src := fixtureSource(t, "Acme/Model", "fedcba9876543210fedcba9876543210")
	imported, err := srcStore.Import(src, ImportOptions{Activate: true, Hash: true})
	if err != nil {
		t.Fatal(err)
	}
	dst, _ := Open(t.TempDir())
	results, err := srcStore.CopyTo(dst, nil, false, true, true, false)
	if err != nil || len(results) != 1 {
		t.Fatalf("copy: %#v %v", results, err)
	}
	results, err = srcStore.CopyTo(dst, nil, false, true, true, false)
	if err != nil || !results[0].Skipped {
		t.Fatalf("idempotent copy: %#v %v", results, err)
	}
	if err := os.WriteFile(filepath.Join(imported.Path, "config.json"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := srcStore.Verify("model", false, false); err == nil {
		t.Fatal("expected tamper verification failure")
	}
}

func TestNameConflictAndRemoveCurrentGuard(t *testing.T) {
	s, _ := Open(t.TempDir())
	one := fixtureSource(t, "A/Same", "aaaaaaaaaaaaaaaa")
	if _, err := s.Import(one, ImportOptions{Activate: true}); err != nil {
		t.Fatal(err)
	}
	two := fixtureSource(t, "B/Same", "bbbbbbbbbbbbbbbb")
	if _, err := s.Import(two, ImportOptions{}); err == nil {
		t.Fatal("expected name conflict")
	}
	if _, err := s.Remove("same@aaaaaaaaaaaa", false, false, false, false); err == nil {
		t.Fatal("expected current removal guard")
	}
}

func TestImportRejectsReservedCurrentVersion(t *testing.T) {
	s, _ := Open(t.TempDir())
	src := fixtureSource(t, "Acme/Current", "current-release")
	if _, err := s.Import(src, ImportOptions{Name: "current", Version: "current"}); err == nil {
		t.Fatal("expected reserved current version to be rejected")
	}
}

func TestConcurrentImportAndResumablePart(t *testing.T) {
	s, _ := Open(t.TempDir())
	src := fixtureSource(t, "Acme/Concurrent", "1234567890abcdef1234567890abcdef")
	name := "concurrent"
	stage := filepath.Join(s.stageDir(), stageKey(name, src), "nested")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "model.bin.part"), []byte("wei"), 0o644); err != nil {
		t.Fatal(err)
	}

	const workers = 8
	results := make(chan ImportResult, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := s.Import(src, ImportOptions{})
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	paths := map[string]bool{}
	for result := range results {
		paths[result.Path] = true
	}
	if len(paths) != 1 {
		t.Fatalf("concurrent imports published %d paths", len(paths))
	}
	versions, err := s.List(name)
	if err != nil || len(versions) != 1 {
		t.Fatalf("versions: %#v, %v", versions, err)
	}
	got, err := os.ReadFile(filepath.Join(versions[0].Path, "nested", "model.bin"))
	if err != nil || string(got) != "weights" {
		t.Fatalf("resumed content %q, %v", got, err)
	}
}

func TestActivateFailureKeepsCurrentAndFullRecord(t *testing.T) {
	s, _ := Open(t.TempDir())
	src := fixtureSource(t, "Acme/Switch", "111111111111aaaa")
	one, err := s.Import(src, ImportOptions{Activate: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Activate("switch@does-not-exist", false); err == nil {
		t.Fatal("expected activation failure")
	}
	resolved, err := s.Resolve("switch")
	if err != nil || resolved.Version != one.Version {
		t.Fatalf("current changed after failed activation: %#v %v", resolved, err)
	}
	m, err := s.Verify("switch", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != m.Files || m.Entries[0].SHA256 == "" {
		t.Fatalf("hashes not recorded: %#v", m.Entries)
	}
}

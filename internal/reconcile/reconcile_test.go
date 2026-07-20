package reconcile

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chieworks/mstore/internal/providers"
	"github.com/chieworks/mstore/internal/source"
	"github.com/chieworks/mstore/internal/store"
)

func modelFixture(t *testing.T, provider, repo, revision string) source.Model {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model.bin"), []byte(repo+revision), 0o644); err != nil {
		t.Fatal(err)
	}
	return source.Model{
		Provider: provider, Repo: repo, Revision: revision, Path: dir, Status: "ready",
	}
}

func scanner(models []source.Model, errs ...error) Scanner {
	return func(string) ([]source.Model, []error) {
		return append([]source.Model(nil), models...), append([]error(nil), errs...)
	}
}

func TestSyncAllReadyAndIdempotent(t *testing.T) {
	repository, _ := store.Open(t.TempDir())
	hf := modelFixture(t, "hf", "Acme/Widget", "111111111111aaaa")
	ms := modelFixture(t, "ms", "Acme/Speaker", "222222222222bbbb")
	incomplete := source.Model{Provider: "hf", Repo: "Acme/Broken", Status: "incomplete"}
	scan := scanner([]source.Model{hf, ms, incomplete})

	report, err := Run(repository, scan, Options{Provider: "all", Jobs: 2})
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Imported != 2 || report.Summary.Failed != 0 {
		t.Fatalf("unexpected report: %#v", report)
	}
	versions, err := repository.List("")
	if err != nil || len(versions) != 2 {
		t.Fatalf("versions: %#v, %v", versions, err)
	}

	report, err = Run(repository, scan, Options{Provider: "all", Jobs: 2})
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Skipped != 2 || report.Summary.Imported != 0 {
		t.Fatalf("idempotent report: %#v", report)
	}
}

func TestProviderFilterIsPassedAndEnforced(t *testing.T) {
	repository, _ := store.Open(t.TempDir())
	hf := modelFixture(t, "hf", "Acme/Widget", "111111111111aaaa")
	ms := modelFixture(t, "ms", "Acme/Speaker", "222222222222bbbb")
	calledWith := ""
	scan := func(provider string) ([]source.Model, []error) {
		calledWith = provider
		return []source.Model{hf, ms}, nil
	}

	report, err := Run(repository, scan, Options{Provider: "hf", Jobs: 1})
	if err != nil || calledWith != "hf" || report.Summary.Imported != 1 {
		t.Fatalf("provider report: %#v, called=%q, err=%v", report, calledWith, err)
	}
	versions, listErr := repository.List("")
	if listErr != nil || len(versions) != 1 || versions[0].Manifest.Source.Provider != "hf" {
		t.Fatalf("versions: %#v, %v", versions, listErr)
	}
}

func TestNewNameCollisionPreventsPartialImport(t *testing.T) {
	repository, _ := store.Open(t.TempDir())
	one := modelFixture(t, "hf", "A/Same", "aaaaaaaaaaaaaaaa")
	two := modelFixture(t, "ms", "B/Same", "bbbbbbbbbbbbbbbb")

	report, err := Run(repository, scanner([]source.Model{one, two}), Options{Provider: "all", Jobs: 2})
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Conflicts != 2 {
		t.Fatalf("expected conflicts, got report=%#v err=%v", report, err)
	}
	versions, listErr := repository.List("")
	if listErr != nil || len(versions) != 0 {
		t.Fatalf("collision imported versions: %#v, %v", versions, listErr)
	}
}

func TestRegisteredNameIsReusedAndSelectionValidated(t *testing.T) {
	repository, _ := store.Open(t.TempDir())
	old := modelFixture(t, "hf", "Acme/Widget", "111111111111aaaa")
	if _, err := repository.Import(old, store.ImportOptions{Name: "custom-widget"}); err != nil {
		t.Fatal(err)
	}
	next := modelFixture(t, "hf", "Acme/Widget", "222222222222bbbb")

	report, err := Run(repository, scanner([]source.Model{next}), Options{
		Provider: "all", Models: []string{"custom-widget"}, Jobs: 1,
	})
	if err != nil || report.Summary.Imported != 1 || report.Results[0].Name != "custom-widget" {
		t.Fatalf("registered sync: %#v, %v", report, err)
	}

	_, err = Run(repository, scanner([]source.Model{next}), Options{
		Provider: "all", Models: []string{"missing"}, Jobs: 1,
	})
	var selectionErr SelectionError
	if !errors.As(err, &selectionErr) {
		t.Fatalf("expected selection error, got %v", err)
	}
}

func TestImportFailureContinues(t *testing.T) {
	repository, _ := store.Open(t.TempDir())
	good := modelFixture(t, "hf", "Acme/Good", "aaaaaaaaaaaaaaaa")
	bad := source.Model{
		Provider: "hf", Repo: "Acme/Bad", Revision: "bbbbbbbbbbbbbbbb",
		Path: filepath.Join(t.TempDir(), "missing"), Status: "ready",
	}
	report, err := Run(repository, scanner([]source.Model{bad, good}), Options{Provider: "all", Jobs: 2})
	if err == nil || report.Summary.Imported != 1 || report.Summary.Failed != 1 {
		t.Fatalf("best effort report: %#v, %v", report, err)
	}
}

func TestDryRunDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	repository, _ := store.Open(filepath.Join(root, "store"))
	model := modelFixture(t, "hf", "Acme/Widget", "aaaaaaaaaaaaaaaa")
	report, err := Run(repository, scanner([]source.Model{model}), Options{
		Provider: "all", Jobs: 1, DryRun: true,
	})
	if err != nil || report.Summary.Planned != 1 {
		t.Fatalf("dry run report: %#v, %v", report, err)
	}
	if _, err := os.Stat(repository.Root); !os.IsNotExist(err) {
		t.Fatalf("dry run created store: %v", err)
	}
}

func TestActivateProviderPreferredAndAmbiguous(t *testing.T) {
	repository, _ := store.Open(t.TempDir())
	one := modelFixture(t, "hf", "Acme/Widget", "111111111111aaaa")
	two := modelFixture(t, "hf", "Acme/Widget", "222222222222bbbb")
	two.Preferred = true
	report, err := Run(repository, scanner([]source.Model{one, two}), Options{
		Provider: "all", Jobs: 2, Activate: true,
	})
	if err != nil || report.Summary.Activated != 1 {
		t.Fatalf("activation report: %#v, %v", report, err)
	}
	current, err := repository.Resolve("widget")
	if err != nil || current.Manifest.Source.Revision != two.Revision {
		t.Fatalf("current: %#v, %v", current, err)
	}

	ambiguousStore, _ := store.Open(t.TempDir())
	two.Preferred = false
	report, err = Run(ambiguousStore, scanner([]source.Model{one, two}), Options{
		Provider: "all", Jobs: 2, Activate: true,
	})
	if err == nil || report.Summary.Imported != 2 || report.Summary.Failed != 1 {
		t.Fatalf("ambiguous report: %#v, %v", report, err)
	}
	if _, resolveErr := ambiguousStore.Resolve("widget"); resolveErr == nil {
		t.Fatal("ambiguous activation unexpectedly created current")
	}
}

func TestActivateAlreadyImportedPreferredRevision(t *testing.T) {
	repository, _ := store.Open(t.TempDir())
	model := modelFixture(t, "hf", "Acme/Widget", "111111111111aaaa")
	model.Preferred = true
	if _, err := Run(repository, scanner([]source.Model{model}), Options{
		Provider: "all", Jobs: 1,
	}); err != nil {
		t.Fatal(err)
	}

	report, err := Run(repository, scanner([]source.Model{model}), Options{
		Provider: "all", Jobs: 1, Activate: true,
	})
	if err != nil || report.Summary.Skipped != 1 || report.Summary.Activated != 1 {
		t.Fatalf("activation report: %#v, %v", report, err)
	}
	current, err := repository.Resolve("widget")
	if err != nil || current.Manifest.Source.Revision != model.Revision {
		t.Fatalf("current: %#v, %v", current, err)
	}
}

func TestActivateUniqueRevisionFallback(t *testing.T) {
	repository, _ := store.Open(t.TempDir())
	model := modelFixture(t, "hf", "Acme/Widget", "111111111111aaaa")
	report, err := Run(repository, scanner([]source.Model{model}), Options{
		Provider: "all", Jobs: 1, Activate: true,
	})
	if err != nil || report.Summary.Imported != 1 || report.Summary.Activated != 1 {
		t.Fatalf("fallback activation report: %#v, %v", report, err)
	}
}

func TestExistingNameOwnerWinsConflict(t *testing.T) {
	repository, _ := store.Open(t.TempDir())
	old := modelFixture(t, "hf", "Acme/Same", "111111111111aaaa")
	if _, err := repository.Import(old, store.ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	next := modelFixture(t, "hf", "Acme/Same", "222222222222bbbb")
	conflict := modelFixture(t, "ms", "Other/Same", "333333333333cccc")

	report, err := Run(repository, scanner([]source.Model{conflict, next}), Options{
		Provider: "all", Jobs: 2,
	})
	var runErr *RunError
	if !errors.As(err, &runErr) || report.Summary.Imported != 1 || report.Summary.Conflict != 1 {
		t.Fatalf("owner conflict report: %#v, %v", report, err)
	}
	versions, listErr := repository.List("same")
	if listErr != nil || len(versions) != 2 {
		t.Fatalf("owner versions: %#v, %v", versions, listErr)
	}
}

func TestProviderScanErrors(t *testing.T) {
	repository, _ := store.Open(t.TempDir())
	missing := providers.ScanError{
		Provider: "ms",
		Err:      &os.PathError{Op: "open", Path: "/missing", Err: os.ErrNotExist},
	}
	broken := providers.ScanError{Provider: "hf", Err: errors.New("permission denied")}
	report, err := Run(repository, scanner(nil, missing, broken), Options{Provider: "all", Jobs: 1})
	if err == nil || report.Summary.Skipped != 1 || report.Summary.Failed != 1 {
		t.Fatalf("scan errors: %#v, %v", report, err)
	}
}

type concurrentRepository struct {
	active int32
	max    int32
}

func (r *concurrentRepository) List(string) ([]store.Version, error) { return nil, nil }

func (r *concurrentRepository) Import(src source.Model, _ store.ImportOptions) (store.ImportResult, error) {
	active := atomic.AddInt32(&r.active, 1)
	for {
		maximum := atomic.LoadInt32(&r.max)
		if active <= maximum || atomic.CompareAndSwapInt32(&r.max, maximum, active) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	atomic.AddInt32(&r.active, -1)
	return store.ImportResult{Name: src.Repo, Version: src.Revision, Path: src.Path}, nil
}

func (r *concurrentRepository) Activate(string, bool) error { return nil }

func TestJobsControlsConcurrency(t *testing.T) {
	var models []source.Model
	for i := range 6 {
		models = append(models, modelFixture(t, "hf", "Acme/Model"+string(rune('A'+i)), "aaaaaaaaaaaa"+string(rune('a'+i))))
	}
	repository := &concurrentRepository{}
	report, err := Run(repository, scanner(models), Options{Provider: "all", Jobs: 3})
	if err != nil || report.Summary.Imported != len(models) {
		t.Fatalf("concurrent report: %#v, %v", report, err)
	}
	if got := atomic.LoadInt32(&repository.max); got < 2 || got > 3 {
		t.Fatalf("max concurrency = %d", got)
	}
	for i := 1; i < len(report.Results); i++ {
		if report.Results[i-1].Source > report.Results[i].Source {
			t.Fatalf("results are not sorted: %#v", report.Results)
		}
	}
}

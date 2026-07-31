package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chieworks/mstore/internal/source"
	"github.com/chieworks/mstore/internal/store"
)

func TestCLIPruneDryRunDoesNotDeleteProviderCaches(t *testing.T) {
	hf := t.TempDir()
	broken := filepath.Join(hf, "models--Acme--Broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	invalid := filepath.Join(hf, "models--Acme--Invalid", "snapshots", "rev")
	if err := os.MkdirAll(invalid, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(invalid, "model.bin")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HF_HUB_CACHE", hf)
	t.Setenv("MODELSCOPE_CACHE", filepath.Join(t.TempDir(), "missing"))

	var out, errOut bytes.Buffer
	if code := run([]string{"--store", t.TempDir(), "prune"}, &out, &errOut); code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "DELETE") || !strings.Contains(out.String(), "incomplete") {
		t.Fatalf("unexpected prune plan: %q", out.String())
	}
	if _, err := os.Stat(broken); err != nil {
		t.Fatalf("dry-run removed incomplete cache: %v", err)
	}
	if _, err := os.Stat(invalid); err != nil {
		t.Fatalf("dry-run removed invalid cache: %v", err)
	}
}

func TestCLIPruneYesDeletesAllAbnormalProviderTargets(t *testing.T) {
	hf := t.TempDir()
	broken := filepath.Join(hf, "models--Acme--Broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	invalid := filepath.Join(hf, "models--Acme--Invalid", "snapshots", "rev")
	if err := os.MkdirAll(invalid, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(invalid, "model.bin")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HF_HUB_CACHE", hf)
	t.Setenv("MODELSCOPE_CACHE", filepath.Join(t.TempDir(), "missing"))

	var out, errOut bytes.Buffer
	if code := run([]string{"--store", t.TempDir(), "prune", "--yes"}, &out, &errOut); code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if _, err := os.Stat(broken); !os.IsNotExist(err) {
		t.Fatalf("incomplete cache remains: %v", err)
	}
	if _, err := os.Stat(invalid); !os.IsNotExist(err) {
		t.Fatalf("invalid cache remains: %v", err)
	}
}

func TestCLIPruneJSONAndCurrentProtection(t *testing.T) {
	hf := t.TempDir()
	invalid := filepath.Join(hf, "models--Acme--Current", "snapshots", "rev")
	repo := filepath.Dir(filepath.Dir(invalid))
	if err := os.MkdirAll(filepath.Join(repo, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(invalid, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(invalid, "model.bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "refs", "main"), []byte("rev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HF_HUB_CACHE", hf)
	t.Setenv("MODELSCOPE_CACHE", filepath.Join(t.TempDir(), "missing"))

	var out, errOut bytes.Buffer
	if code := run([]string{"--store", t.TempDir(), "prune", "--yes", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var report pruneReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.DryRun || len(report.Items) != 1 || report.Items[0].Action != "skip" {
		t.Fatalf("unexpected report: %#v", report)
	}
	if _, err := os.Stat(invalid); err != nil {
		t.Fatalf("protected current cache removed: %v", err)
	}
	out.Reset()
	errOut.Reset()
	if code := run([]string{"--store", t.TempDir(), "prune", "--yes", "--force"}, &out, &errOut); code != 0 {
		t.Fatalf("force code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if _, err := os.Stat(invalid); !os.IsNotExist(err) {
		t.Fatalf("force did not remove protected current cache: %v", err)
	}
}

func TestCLIPruneKeepsImportedOwnerAndProtectsReadyConflict(t *testing.T) {
	hf := t.TempDir()
	conflict := filepath.Join(hf, "models--Other--Widget", "snapshots", "rev")
	if err := os.MkdirAll(conflict, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conflict, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HF_HUB_CACHE", hf)
	t.Setenv("MODELSCOPE_CACHE", filepath.Join(t.TempDir(), "missing"))

	storeRoot := t.TempDir()
	s, err := store.Open(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	ownerDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ownerDir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(source.Model{Provider: "hf", Repo: "Acme/Widget", Revision: "owner", Path: ownerDir, Status: "ready"}, store.ImportOptions{Activate: true}); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := run([]string{"--store", storeRoot, "prune", "--yes", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var report pruneReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	var kept, skipped bool
	for _, item := range report.Items {
		if item.Repo == "Acme/Widget" && item.Action == "keep" {
			kept = true
		}
		if item.Repo == "Other/Widget" && item.Status == "conflict" && item.Action == "skip" && strings.Contains(item.Reason, "ready") {
			skipped = true
		}
	}
	if !kept || !skipped {
		t.Fatalf("unexpected conflict report: %#v", report)
	}
	if _, err := os.Stat(conflict); err != nil {
		t.Fatalf("ready conflict cache removed: %v", err)
	}
	if _, err := s.Resolve("widget"); err != nil {
		t.Fatalf("imported owner was removed: %v", err)
	}
}

func TestCLIPruneSkipsMultipleUnimportedNameConflicts(t *testing.T) {
	hf := t.TempDir()
	hfSnapshot := filepath.Join(hf, "models--Acme--Same", "snapshots", "rev")
	if err := os.MkdirAll(hfSnapshot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hfSnapshot, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	ms := t.TempDir()
	msSnapshot := filepath.Join(ms, "models", "Other--Same", "snapshots", "rev")
	if err := os.MkdirAll(msSnapshot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(msSnapshot, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HF_HUB_CACHE", hf)
	t.Setenv("MODELSCOPE_CACHE", ms)

	var out, errOut bytes.Buffer
	if code := run([]string{"--store", t.TempDir(), "prune", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var report pruneReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Items) != 2 {
		t.Fatalf("items=%#v", report.Items)
	}
	for _, item := range report.Items {
		if item.Status != "conflict" || item.Action != "skip" {
			t.Fatalf("unexpected item: %#v", item)
		}
	}
	if strings.Contains(out.String(), `"action":"delete"`) {
		t.Fatalf("ambiguous source was scheduled for deletion: %s", out.String())
	}
}

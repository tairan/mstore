package modelconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chieworks/mstore/internal/source"
)

func TestReadDefaultsEnabledToFalseAndRejectsActivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.toml")
	if err := os.WriteFile(path, []byte("schema = 1\n\n[[models]]\nsource = \"hf:Acme/Widget@0123456789abcdef\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	selections, err := Selections(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(selections) != 0 {
		t.Fatalf("selections = %#v", selections)
	}

	if err := os.WriteFile(path, []byte("schema = 1\n\n[[models]]\nsource = \"hf:Acme/Widget@0123456789abcdef\"\nenabled = true\nactivate = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil || !strings.Contains(err.Error(), "activate") {
		t.Fatalf("activate field error = %v", err)
	}
}

func TestExportWritesDisabledReadyModels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.toml")
	models := []source.Model{
		{Provider: "hf", Repo: "Acme/Ready", Revision: "0123456789abcdef", Status: "ready"},
		{Provider: "hf", Repo: "Acme/Incomplete", Revision: "abcdef", Status: "incomplete"},
	}
	if count, err := Export(path, models, false); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		"schema = 1", "source = \"hf:Acme/Ready@0123456789abcdef\"", "enabled = false", "name = \"ready\"",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("export missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Incomplete") || strings.Contains(got, "activate") {
		t.Fatalf("unexpected export:\n%s", got)
	}
	if _, err := Export(path, models, false); err == nil {
		t.Fatal("expected overwrite protection")
	}
}

func TestExportUsesFallbackNameWhenDefaultNameIsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.toml")
	models := []source.Model{{Provider: "hf", Repo: "Acme/模型", Revision: "0123456789abcdef", Status: "ready"}}
	if count, err := Export(path, models, false); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	file, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Models) != 1 || !strings.HasPrefix(file.Models[0].Name, "model-") {
		t.Fatalf("models=%#v", file.Models)
	}
}

func TestExportImportedWritesEnabledModels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.toml")
	models := []ImportedModel{
		{Source: "ms:Qwen/Demo@v1.2.3", Name: "demo"},
		{Source: "hf:Acme/Widget@0123456789abcdef", Name: "custom-widget"},
	}
	if count, err := ExportImported(path, models, false); err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	file, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Models) != 2 {
		t.Fatalf("models=%#v", file.Models)
	}
	for _, model := range file.Models {
		if !model.Enabled {
			t.Fatalf("imported model is disabled: %#v", model)
		}
	}
	if file.Models[0].Source != "hf:Acme/Widget@0123456789abcdef" || file.Models[0].Name != "custom-widget" {
		t.Fatalf("models are not sorted by source: %#v", file.Models)
	}
}

func TestExportImportedRejectsDuplicateSourceNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.toml")
	_, err := ExportImported(path, []ImportedModel{
		{Source: "hf:Acme/Widget@0123456789abcdef", Name: "widget-a"},
		{Source: "hf:Acme/Widget@0123456789abcdef", Name: "widget-b"},
	}, false)
	if err == nil || !strings.Contains(err.Error(), "multiple model names") {
		t.Fatalf("duplicate source error = %v", err)
	}
}

func TestOutputPathAddsTomlExtensionOnlyWhenMissing(t *testing.T) {
	for input, want := range map[string]string{
		"models":      "models.toml",
		"models.toml": "models.toml",
		"models.yaml": "models.yaml",
	} {
		if got := OutputPath(input); got != want {
			t.Errorf("OutputPath(%q) = %q, want %q", input, got, want)
		}
	}
}

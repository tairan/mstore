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
	if err := Export(path, models, false); err != nil {
		t.Fatal(err)
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
	if err := Export(path, models, false); err == nil {
		t.Fatal("expected overwrite protection")
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

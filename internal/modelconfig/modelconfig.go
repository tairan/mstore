// Package modelconfig reads and writes the editable model selection file.
package modelconfig

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/chieworks/mstore/internal/naming"
	"github.com/chieworks/mstore/internal/source"
)

const Schema = 1

// OutputPath adds the standard TOML extension when the requested output has
// no filename extension.
func OutputPath(path string) string {
	if filepath.Ext(path) == "" {
		return path + ".toml"
	}
	return path
}

type File struct {
	Schema   int      `toml:"schema"`
	Defaults Defaults `toml:"defaults"`
	Models   []Model  `toml:"models"`
}

type Defaults struct {
	Hash bool `toml:"hash"`
}

type Model struct {
	Source  string `toml:"source"`
	Enabled bool   `toml:"enabled"`
	Name    string `toml:"name"`
}

// ImportedModel is a model already published in the mstore store.
type ImportedModel struct {
	Source string
	Name   string
}

// Selection is one enabled model ready to pass to reconciliation.
type Selection struct {
	Source source.Ref
	Name   string
}

func Read(path string) (File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	var file File
	if err := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields().Decode(&file); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			fields := make([]string, 0, len(strict.Errors))
			for _, decodeErr := range strict.Errors {
				fields = append(fields, strings.Join(decodeErr.Key(), "."))
			}
			return File{}, fmt.Errorf("decode model config: unknown field(s): %s", strings.Join(fields, ", "))
		}
		return File{}, fmt.Errorf("decode model config: %w", err)
	}
	if err := Validate(file); err != nil {
		return File{}, err
	}
	return file, nil
}

func Validate(file File) error {
	if file.Schema != Schema {
		return fmt.Errorf("unsupported model config schema %d (expected %d)", file.Schema, Schema)
	}
	seenSource := map[string]bool{}
	for i, model := range file.Models {
		at := fmt.Sprintf("models[%d]", i)
		ref, err := source.ParseRef(model.Source)
		if err != nil {
			return fmt.Errorf("%s.source: %w", at, err)
		}
		if ref.Revision == "" {
			return fmt.Errorf("%s.source must include an immutable revision", at)
		}
		if seenSource[model.Source] {
			return fmt.Errorf("%s.source duplicates %q", at, model.Source)
		}
		seenSource[model.Source] = true
		name := model.Name
		if name == "" {
			name, err = naming.Normalize(ref.Repo)
			if err != nil {
				return fmt.Errorf("%s.source: %w", at, err)
			}
		} else if err := naming.Validate(name); err != nil {
			return fmt.Errorf("%s.name: %w", at, err)
		}
	}
	return nil
}

func Selections(file File) ([]Selection, error) {
	if err := Validate(file); err != nil {
		return nil, err
	}
	selections := make([]Selection, 0, len(file.Models))
	for _, model := range file.Models {
		if !model.Enabled {
			continue
		}
		ref, _ := source.ParseRef(model.Source)
		name := model.Name
		if name == "" {
			name, _ = naming.Normalize(ref.Repo)
		}
		selections = append(selections, Selection{Source: ref, Name: name})
	}
	sort.Slice(selections, func(i, j int) bool {
		return selections[i].Source.Provider+":"+selections[i].Source.Repo+"@"+selections[i].Source.Revision <
			selections[j].Source.Provider+":"+selections[j].Source.Repo+"@"+selections[j].Source.Revision
	})
	return selections, nil
}

// Export writes a deliberately conservative candidate file: no model is enabled.
// It returns the number of ready models written to the file.
func Export(path string, models []source.Model, overwrite bool) (int, error) {
	entries := make([]exportEntry, 0, len(models))
	for _, model := range models {
		if model.Status != "ready" {
			continue
		}
		name, err := naming.Normalize(model.Repo)
		if err != nil {
			name = fallbackName(model.Ref())
			entries = append(entries, exportEntry{
				Source: model.Ref(), Name: name, Comment: fmt.Sprintf(
					"Default name could not be derived for %s; choose a descriptive name before enabling it.", model.Ref()),
			})
			continue
		}
		entries = append(entries, exportEntry{Source: model.Ref(), Name: name})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Source < entries[j].Source })
	return write(path, entries, overwrite)
}

// ExportImported writes all imported source revisions as enabled selections.
// A source imported under multiple names cannot be represented by the v1
// config format, so it returns an error instead of silently dropping a name.
func ExportImported(path string, models []ImportedModel, overwrite bool) (int, error) {
	entries := make([]exportEntry, 0, len(models))
	seen := map[string]string{}
	for _, model := range models {
		ref, err := source.ParseRef(model.Source)
		if err != nil {
			return 0, fmt.Errorf("imported model %q: %w", model.Source, err)
		}
		if ref.Revision == "" {
			return 0, fmt.Errorf("imported model %q must include an immutable revision", model.Source)
		}
		if prior, ok := seen[model.Source]; ok {
			if prior == model.Name {
				continue
			}
			return 0, fmt.Errorf("conflict: imported source %q is used by multiple model names %q and %q", model.Source, prior, model.Name)
		}
		if err := naming.Validate(model.Name); err != nil {
			return 0, fmt.Errorf("imported model %q: %w", model.Source, err)
		}
		seen[model.Source] = model.Name
		entries = append(entries, exportEntry{Source: model.Source, Name: model.Name, Enabled: true})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Source != entries[j].Source {
			return entries[i].Source < entries[j].Source
		}
		return entries[i].Name < entries[j].Name
	})
	return write(path, entries, overwrite)
}

type exportEntry struct {
	Source  string
	Enabled bool
	Name    string
	Comment string
}

func write(path string, entries []exportEntry, overwrite bool) (int, error) {
	var b strings.Builder
	b.WriteString("# mstore model selection file (schema 1).\n")
	b.WriteString("# Set enabled = true for models that mstore sync should publish.\n")
	b.WriteString("schema = 1\n\n[defaults]\nhash = false\n")
	for _, entry := range entries {
		if entry.Comment != "" {
			fmt.Fprintf(&b, "\n# %s\n", entry.Comment)
		}
		fmt.Fprintf(&b, "\n[[models]]\nsource = %q\nenabled = %t\nname = %q\n", entry.Source, entry.Enabled, entry.Name)
	}
	data := []byte(b.String())
	if overwrite {
		return len(entries), os.WriteFile(path, data, 0o644)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return 0, fmt.Errorf("warning: config %s already exists; refusing to overwrite (use --overwrite)", path)
		}
		return 0, err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	return len(entries), nil
}

func fallbackName(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return fmt.Sprintf("model-%x", sum[:6])
}

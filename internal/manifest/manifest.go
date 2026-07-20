package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const Filename = ".mstore.json"

type Source struct {
	Provider string `json:"provider"`
	Repo     string `json:"repo"`
	Revision string `json:"revision"`
}

type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

type Manifest struct {
	Schema     int       `json:"schema"`
	Name       string    `json:"name"`
	Version    string    `json:"version"`
	Source     Source    `json:"source"`
	Files      int       `json:"files"`
	Bytes      int64     `json:"bytes"`
	ImportedAt time.Time `json:"imported_at"`
	Entries    []File    `json:"entries,omitempty"`
}

func (m Manifest) IdentityEqual(other Manifest) bool {
	return m.Source.Provider == other.Source.Provider &&
		m.Source.Repo == other.Source.Repo &&
		m.Source.Revision == other.Source.Revision
}

func Read(dir string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(filepath.Join(dir, Filename))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("decode manifest: %w", err)
	}
	if m.Schema != 1 || m.Name == "" || m.Version == "" ||
		m.Source.Provider == "" || m.Source.Repo == "" || m.Source.Revision == "" {
		return m, fmt.Errorf("invalid manifest")
	}
	return m, nil
}

func Write(dir string, m Manifest) error {
	m.Schema = 1
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := filepath.Join(dir, Filename+".part")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, Filename))
}

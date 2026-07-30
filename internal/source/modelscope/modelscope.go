package modelscope

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chieworks/mstore/internal/fsutil"
	"github.com/chieworks/mstore/internal/source"
)

func CacheRoot() (string, error) {
	if p := os.Getenv("MODELSCOPE_CACHE"); p != "" {
		p, err := expand(p)
		return filepath.Join(p, "models"), err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "modelscope", "models"), nil
}

func Scan(root string) ([]source.Model, error) {
	repositories, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []source.Model
	for _, repository := range repositories {
		if !repository.IsDir() || strings.HasPrefix(repository.Name(), ".") {
			continue
		}
		parts := strings.SplitN(repository.Name(), "--", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		repo := parts[0] + "/" + parts[1]
		snapshots, err := os.ReadDir(filepath.Join(root, repository.Name(), "snapshots"))
		if err != nil {
			out = append(out, source.Model{
				Provider: "ms", Repo: repo, Status: "incomplete",
				Error: fmt.Sprintf("read snapshots: %v", err),
			})
			continue
		}
		for _, snapshot := range snapshots {
			if !snapshot.IsDir() || snapshot.Name() == "" || strings.HasPrefix(snapshot.Name(), ".") {
				continue
			}
			dir := filepath.Join(root, repository.Name(), "snapshots", snapshot.Name())
			m := source.Model{
				Provider:  "ms",
				Repo:      repo,
				Revision:  snapshot.Name(),
				Path:      dir,
				Status:    "ready",
				Preferred: snapshot.Name() == "master",
			}
			if _, _, scanErr := fsutil.Scan(dir, false); scanErr != nil {
				m.Status, m.Error = "invalid", scanErr.Error()
			}
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref() < out[j].Ref() })
	return out, nil
}

func Resolve(r source.Ref) (source.Model, error) {
	root, err := CacheRoot()
	if err != nil {
		return source.Model{}, err
	}
	models, err := Scan(root)
	if err != nil {
		return source.Model{}, err
	}
	var matches []source.Model
	for _, m := range models {
		if m.Provider != r.Provider || m.Repo != r.Repo {
			continue
		}
		if r.Revision != "" && m.Revision == r.Revision {
			if m.Status != "ready" {
				return source.Model{}, fmt.Errorf("%s: %s", m.Status, m.Error)
			}
			return m, nil
		}
		if source.Match(m, r) && m.Status == "ready" {
			matches = append(matches, m)
		}
	}
	if len(matches) == 0 {
		return source.Model{}, fmt.Errorf("source not found or incomplete: ms:%s", r.Repo)
	}
	if len(matches) > 1 && r.Revision == "" {
		return source.Model{}, fmt.Errorf("multiple revisions found for ms:%s; specify @revision", r.Repo)
	}
	if len(matches) > 1 {
		return source.Model{}, fmt.Errorf("revision prefix is ambiguous")
	}
	return matches[0], nil
}

func expand(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return filepath.Abs(path)
}

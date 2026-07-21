package modelscope

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/chieworks/mstore/internal/fsutil"
	"github.com/chieworks/mstore/internal/source"
)

var revisionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+:-]{0,255}$`)

func CacheRoot() (string, error) {
	if p := os.Getenv("MODELSCOPE_CACHE"); p != "" {
		p, err := expand(p)
		return filepath.Join(p, "models"), err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "modelscope", "hub", "models"), nil
}

func Scan(root string) ([]source.Model, error) {
	// The supported root itself must be named models. This deliberately rejects
	// the historical hub/<namespace>/<repo> layout instead of guessing.
	if filepath.Base(filepath.Clean(root)) != "models" {
		return nil, fmt.Errorf("unsupported ModelScope cache layout: expected .../models/<namespace>/<repo>")
	}
	namespaces, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []source.Model
	for _, ns := range namespaces {
		if !ns.IsDir() || strings.HasPrefix(ns.Name(), ".") {
			continue
		}
		repos, err := os.ReadDir(filepath.Join(root, ns.Name()))
		if err != nil {
			continue
		}
		for _, repo := range repos {
			if !repo.IsDir() || strings.HasPrefix(repo.Name(), ".") {
				continue
			}
			dir := filepath.Join(root, ns.Name(), repo.Name())
			m := source.Model{Provider: "ms", Repo: ns.Name() + "/" + repo.Name(), Path: dir, Status: "ready"}
			b, readErr := os.ReadFile(filepath.Join(dir, ".mv"))
			if readErr != nil {
				m.Status, m.Error = "incomplete", "missing .mv"
			} else {
				m.Revision = parseRevision(string(b))
				if !revisionPattern.MatchString(m.Revision) {
					m.Status, m.Error = "invalid", "invalid .mv revision"
				} else if _, _, scanErr := fsutil.Scan(dir, false); scanErr != nil {
					m.Status, m.Error = "invalid", scanErr.Error()
				} else {
					m.Preferred = true
				}
			}
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref() < out[j].Ref() })
	return out, nil
}

// ModelScope writes either a plain revision or
// "Revision:<revision>,CreatedAt:<timestamp>" to .mv.
func parseRevision(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "Revision:") {
		s = strings.TrimPrefix(s, "Revision:")
		if i := strings.IndexByte(s, ','); i >= 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
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
	for _, m := range models {
		if source.Match(m, r) {
			if m.Status != "ready" {
				return source.Model{}, fmt.Errorf("%s: %s", m.Status, m.Error)
			}
			return m, nil
		}
	}
	return source.Model{}, fmt.Errorf("source not found: ms:%s", r.Repo)
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

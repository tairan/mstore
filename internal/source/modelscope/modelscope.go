package modelscope

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/chieworks/mstore/internal/fsutil"
	"github.com/chieworks/mstore/internal/source"
)

// CacheRoot returns the ModelScope model directory, not its parent cache.
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

// RepoPath validates and encodes a canonical namespace/repository reference.
// ModelScope encodes dots in the repository directory as three underscores.
func RepoPath(root, repo string) (string, error) {
	namespace, encoded, err := encodeRepoPath(repo)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, namespace, encoded), nil
}

func encodeRepoPath(repo string) (string, string, error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || !validPathPart(parts[0]) || !validPathPart(parts[1]) {
		return "", "", fmt.Errorf("invalid ModelScope repository %q", repo)
	}
	return parts[0], strings.ReplaceAll(parts[1], ".", "___"), nil
}

func decodeRepoPath(namespace, encoded string) (string, error) {
	if !validPathPart(namespace) || !validPathPart(encoded) || encoded == "snapshots" {
		return "", fmt.Errorf("invalid ModelScope repository path")
	}
	repo := strings.ReplaceAll(encoded, "___", ".")
	if !validPathPart(repo) {
		return "", fmt.Errorf("invalid ModelScope repository path")
	}
	return namespace + "/" + repo, nil
}

func validPathPart(part string) bool {
	return part != "" && part != "." && part != ".." && utf8.ValidString(part) &&
		strings.IndexFunc(part, unicode.IsControl) < 0 && !strings.ContainsAny(part, `/\\`)
}

func validRevision(revision string) bool {
	return validPathPart(revision)
}

func readRevision(path string) (string, error) {
	b, err := os.ReadFile(filepath.Join(path, ".mv"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", os.ErrNotExist
		}
		return "", fmt.Errorf("read .mv: %w", err)
	}
	raw := strings.TrimSpace(string(b))
	if strings.HasPrefix(raw, "Revision:") {
		fields := strings.Split(raw, ",")
		if len(fields) != 2 || !strings.HasPrefix(fields[1], "CreatedAt:") {
			return "", fmt.Errorf("invalid .mv metadata")
		}
		raw = strings.TrimPrefix(fields[0], "Revision:")
	}
	if !validRevision(raw) || strings.TrimSpace(raw) != raw {
		return "", fmt.Errorf("invalid ModelScope revision")
	}
	return raw, nil
}

func Scan(root string) ([]source.Model, error) {
	namespaces, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []source.Model
	for _, namespace := range namespaces {
		if !namespace.IsDir() || !validPathPart(namespace.Name()) {
			continue
		}
		namespacePath := filepath.Join(root, namespace.Name())
		// A direct <namespace>--<repo>/snapshots tree is the removed layout.
		if strings.Contains(namespace.Name(), "--") {
			if info, readErr := os.Stat(filepath.Join(namespacePath, "snapshots")); readErr == nil && info.IsDir() {
				continue
			}
		}
		repositories, readErr := os.ReadDir(namespacePath)
		if readErr != nil {
			continue
		}
		for _, repository := range repositories {
			if !repository.IsDir() {
				continue
			}
			repo, decodeErr := decodeRepoPath(namespace.Name(), repository.Name())
			if decodeErr != nil {
				continue
			}
			dir := filepath.Join(namespacePath, repository.Name())
			m := source.Model{Provider: "ms", Repo: repo, Path: dir}
			revision, revisionErr := readRevision(dir)
			if revisionErr != nil {
				if revisionErr == os.ErrNotExist {
					m.Status, m.Error = "incomplete", "missing .mv"
				} else {
					m.Status, m.Error = "invalid", revisionErr.Error()
				}
				out = append(out, m)
				continue
			}
			m.Revision, m.Preferred = revision, revision == "master"
			if _, _, scanErr := fsutil.Scan(dir, false); scanErr != nil {
				m.Status, m.Error = "invalid", scanErr.Error()
			} else {
				m.Status = "ready"
			}
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref() < out[j].Ref() })
	return out, nil
}

func Resolve(r source.Ref) (source.Model, error) {
	if r.Revision != "" && !validRevision(r.Revision) {
		return source.Model{}, fmt.Errorf("invalid ModelScope revision %q", r.Revision)
	}
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

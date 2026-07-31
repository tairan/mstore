package modelscope

import (
	"errors"
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
	if len(parts) != 2 || !validRepoPart(parts[0]) || !validRepoPart(parts[1]) {
		return "", "", fmt.Errorf("invalid ModelScope repository %q", repo)
	}
	return strings.ReplaceAll(parts[0], ".", "___"), strings.ReplaceAll(parts[1], ".", "___"), nil
}

func decodeRepoPath(namespace, encoded string) (string, error) {
	if !validPathPart(namespace) || !validPathPart(encoded) {
		return "", fmt.Errorf("invalid ModelScope repository path")
	}
	namespace = strings.ReplaceAll(namespace, "___", ".")
	repo := strings.ReplaceAll(encoded, "___", ".")
	if !validRepoPart(namespace) || !validRepoPart(repo) {
		return "", fmt.Errorf("invalid ModelScope repository path")
	}
	return namespace + "/" + repo, nil
}

func validPathPart(part string) bool {
	return part != "" && part != "." && part != ".." && utf8.ValidString(part) &&
		strings.IndexFunc(part, unicode.IsControl) < 0 && !strings.ContainsAny(part, `/\\`)
}

func validRepoPart(part string) bool {
	return validPathPart(part) && !strings.HasPrefix(part, ".")
}

func validRevision(revision string) bool {
	return validPathPart(revision) && !strings.HasPrefix(revision, ".") &&
		revision != "current" && !strings.ContainsRune(revision, '@')
}

type revisionReadError struct{ err error }

func (e *revisionReadError) Error() string { return e.err.Error() }
func (e *revisionReadError) Unwrap() error { return e.err }

func readRevision(path string) (string, error) {
	b, err := os.ReadFile(filepath.Join(path, ".mv"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", os.ErrNotExist
		}
		return "", &revisionReadError{err: fmt.Errorf("read .mv: %w", err)}
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
		if !namespace.IsDir() || !validRepoPart(namespace.Name()) {
			continue
		}
		namespacePath := filepath.Join(root, namespace.Name())
		// A direct <namespace>--<repo>/snapshots tree is the removed layout.
		repositories, readErr := os.ReadDir(namespacePath)
		if readErr != nil {
			return nil, fmt.Errorf("read ModelScope namespace %s: %w", namespace.Name(), readErr)
		}
		if isLegacyRepository(namespacePath, namespace.Name(), repositories) {
			continue
		}
		for _, repository := range repositories {
			if !repository.IsDir() || !validRepoPart(repository.Name()) {
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
				var readErr *revisionReadError
				if errors.As(revisionErr, &readErr) {
					return nil, fmt.Errorf("read ModelScope repository %s: %w", repo, readErr)
				}
				if revisionErr == os.ErrNotExist {
					m.Status, m.Error = "incomplete", "missing .mv"
				} else {
					m.Status, m.Error = "invalid", revisionErr.Error()
				}
				out = append(out, m)
				continue
			}
			m.Revision, m.Preferred = revision, true
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

func isLegacyRepository(namespacePath, namespace string, repositories []os.DirEntry) bool {
	if !strings.Contains(namespace, "--") {
		return false
	}
	if len(repositories) != 1 || repositories[0].Name() != "snapshots" || !repositories[0].IsDir() {
		return false
	}
	snapshots := filepath.Join(namespacePath, "snapshots")
	info, err := os.Stat(snapshots)
	if err != nil || !info.IsDir() {
		return false
	}
	// In the removed layout, snapshots is structural and has revision
	// subdirectories. A new repository named snapshots has its .mv at this
	// level, so it remains scannable.
	if _, err := os.Stat(filepath.Join(snapshots, ".mv")); err == nil {
		return false
	}
	entries, err := os.ReadDir(snapshots)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return true
		}
	}
	return false
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

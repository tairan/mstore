package huggingface

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chieworks/mstore/internal/fsutil"
	"github.com/chieworks/mstore/internal/source"
)

func CacheRoot() (string, error) {
	if p := os.Getenv("HF_HUB_CACHE"); p != "" {
		return expand(p)
	}
	if p := os.Getenv("HF_HOME"); p != "" {
		p, err := expand(p)
		return filepath.Join(p, "hub"), err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "huggingface", "hub"), nil
}

func Scan(root string) ([]source.Model, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []source.Model
	for _, repoDir := range entries {
		if !repoDir.IsDir() || !strings.HasPrefix(repoDir.Name(), "models--") {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(repoDir.Name(), "models--"), "--")
		if len(parts) < 2 {
			continue
		}
		repo := strings.Join(parts[:len(parts)-1], "/") + "/" + parts[len(parts)-1]
		snapshots := filepath.Join(root, repoDir.Name(), "snapshots")
		revisions, readErr := os.ReadDir(snapshots)
		if readErr != nil {
			out = append(out, source.Model{Provider: "hf", Repo: repo, Status: "incomplete", Error: "missing snapshots"})
			continue
		}
		for _, rev := range revisions {
			if !rev.IsDir() || rev.Name() == "" {
				continue
			}
			m := source.Model{Provider: "hf", Repo: repo, Revision: rev.Name(), Path: filepath.Join(snapshots, rev.Name()), Status: "ready"}
			if scanErr := validateSnapshot(m.Path, filepath.Join(root, repoDir.Name())); scanErr != nil {
				m.Status, m.Error = "invalid", scanErr.Error()
			}
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref() < out[j].Ref() })
	return out, nil
}

func validateSnapshot(snapshot, repoRoot string) error {
	blobs, err := filepath.Abs(filepath.Join(repoRoot, "blobs"))
	if err != nil {
		return err
	}
	if err := filepath.WalkDir(snapshot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink == 0 {
			return nil
		}
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("dangling symlink %s: %w", path, err)
		}
		target, err = filepath.Abs(target)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(blobs, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("snapshot symlink points outside blobs: %s", path)
		}
		return nil
	}); err != nil {
		return err
	}
	_, _, err = fsutil.Scan(snapshot, false)
	return err
}

func Resolve(r source.Ref) (source.Model, error) {
	root, err := CacheRoot()
	if err != nil {
		return source.Model{}, err
	}
	if r.Revision != "" {
		for _, part := range strings.Split(r.Revision, "/") {
			if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `\`) {
				return source.Model{}, fmt.Errorf("invalid Hugging Face ref %q", r.Revision)
			}
		}
		repoDir := "models--" + strings.ReplaceAll(r.Repo, "/", "--")
		refPath := filepath.Join(append([]string{root, repoDir, "refs"}, strings.Split(r.Revision, "/")...)...)
		if b, readErr := os.ReadFile(refPath); readErr == nil {
			resolved := strings.TrimSpace(string(b))
			if resolved == "" || strings.ContainsAny(resolved, `/\`) {
				return source.Model{}, fmt.Errorf("invalid Hugging Face ref %q", r.Revision)
			}
			r.Revision = resolved
		}
	}
	models, err := Scan(root)
	if err != nil {
		return source.Model{}, fmt.Errorf("Hugging Face cache: %w", err)
	}
	var matches []source.Model
	for _, m := range models {
		if source.Match(m, r) && m.Status == "ready" {
			matches = append(matches, m)
		}
	}
	if len(matches) == 0 {
		return source.Model{}, fmt.Errorf("source not found or incomplete: hf:%s", r.Repo)
	}
	if len(matches) > 1 && r.Revision == "" {
		return source.Model{}, fmt.Errorf("multiple revisions found for hf:%s; specify @revision", r.Repo)
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

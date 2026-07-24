package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/chieworks/mstore/internal/copier"
	"github.com/chieworks/mstore/internal/fsutil"
	"github.com/chieworks/mstore/internal/lock"
	"github.com/chieworks/mstore/internal/manifest"
	"github.com/chieworks/mstore/internal/naming"
	"github.com/chieworks/mstore/internal/source"
)

type Store struct{ Root string }

type ImportOptions struct {
	Name     string
	Version  string
	Activate bool
	Hash     bool
	DryRun   bool
}

type ImportResult struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Path    string `json:"path"`
	Skipped bool   `json:"skipped"`
}

type Version struct {
	Name     string            `json:"name"`
	Version  string            `json:"version"`
	Path     string            `json:"path"`
	Current  bool              `json:"current"`
	Manifest manifest.Manifest `json:"manifest"`
}

func DefaultRoot() (string, error) {
	if p := os.Getenv("MSTORE_HOME"); p != "" {
		return expand(p)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "models"), nil
}

func Open(root string) (*Store, error) {
	var err error
	if root == "" {
		root, err = DefaultRoot()
	} else {
		root, err = expand(root)
	}
	if err != nil {
		return nil, err
	}
	return &Store{Root: filepath.Clean(root)}, nil
}

func (s *Store) Init() error {
	for _, dir := range []string{s.Root, s.stageDir(), s.lockDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Import(src source.Model, opts ImportOptions) (ImportResult, error) {
	var result ImportResult
	if src.Status != "ready" {
		return result, fmt.Errorf("source is %s: %s", src.Status, src.Error)
	}
	name := opts.Name
	if name == "" {
		var err error
		name, err = naming.Normalize(src.Repo)
		if err != nil {
			return result, err
		}
	} else if err := naming.Validate(name); err != nil {
		return result, err
	}
	result.Name = name

	before, bytes, err := fsutil.Scan(src.Path, opts.Hash)
	if err != nil {
		return result, fmt.Errorf("scan source: %w", err)
	}
	version, existing, err := s.chooseVersion(name, src, opts.Version, before, bytes)
	if err != nil {
		return result, err
	}
	result.Version = version
	result.Path = filepath.Join(s.Root, name, version)
	if opts.DryRun {
		if !existing {
			err = s.checkNameIdentity(name, src)
		}
		result.Skipped = existing
		return result, err
	}
	if err := s.Init(); err != nil {
		return result, err
	}
	l, err := lock.Acquire(s.lockDir(), name, 30*time.Second)
	if err != nil {
		return result, err
	}
	defer l.Release()
	// Re-check after acquiring the model lock; another process may have
	// published the identity while this process was scanning the source.
	version, existing, err = s.chooseVersion(name, src, opts.Version, before, bytes)
	if err != nil {
		return result, err
	}
	result.Version = version
	result.Path = filepath.Join(s.Root, name, version)
	if existing {
		result.Skipped = true
		if opts.Hash {
			stored, storedBytes, scanErr := fsutil.Scan(result.Path, true)
			if scanErr != nil {
				return result, fmt.Errorf("scan existing version: %w", scanErr)
			}
			if storedBytes != bytes || !fsutil.SameTree(stored, before, true) {
				return result, fmt.Errorf("existing version differs from source content; refusing to record hashes")
			}
			m, readErr := manifest.Read(result.Path)
			if readErr != nil {
				return result, readErr
			}
			if !manifestHasCompleteHashes(m) {
				m.Entries = before
				if err := manifest.Write(result.Path, m); err != nil {
					return result, err
				}
			}
		}
		if opts.Activate {
			err = s.activateLocked(name, version, true)
		}
		return result, err
	}
	if err := s.checkNameIdentity(name, src); err != nil {
		return result, err
	}
	if err := ensureFreeSpace(s.Root, bytes); err != nil {
		return result, err
	}

	stage := filepath.Join(s.stageDir(), stageKey(name, src))
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return result, err
	}
	if err := copier.CopyInventory(src.Path, stage, before); err != nil {
		return result, err
	}
	after, afterBytes, err := fsutil.Scan(src.Path, opts.Hash)
	if err != nil || bytes != afterBytes || !fsutil.SameTree(before, after, opts.Hash) {
		return result, fmt.Errorf("source changed during copy; publication canceled")
	}
	staged, stagedBytes, err := fsutil.Scan(stage, opts.Hash)
	if err != nil || bytes != stagedBytes || !fsutil.SameTree(before, staged, opts.Hash) {
		return result, fmt.Errorf("staging verification failed")
	}
	m := manifest.Manifest{
		Name: name, Version: version,
		Source:     manifest.Source{Provider: src.Provider, Repo: src.Repo, Revision: src.Revision},
		Files:      len(before),
		Bytes:      bytes,
		ImportedAt: time.Now().UTC(),
	}
	if opts.Hash {
		m.Entries = staged
	}
	if err := manifest.Write(stage, m); err != nil {
		return result, err
	}
	if err := fsutil.SyncDir(stage); err != nil {
		return result, err
	}
	if err := os.MkdirAll(filepath.Dir(result.Path), 0o755); err != nil {
		return result, err
	}
	if err := os.Rename(stage, result.Path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return result, fmt.Errorf("version appeared concurrently: %s", result.Path)
		}
		return result, err
	}
	if err := fsutil.SyncDir(filepath.Dir(result.Path)); err != nil {
		return result, err
	}
	if opts.Activate {
		if err := s.activateLocked(name, version, true); err != nil {
			return result, err
		}
	}
	return result, nil
}

func manifestHasCompleteHashes(m manifest.Manifest) bool {
	if len(m.Entries) != m.Files || len(m.Entries) == 0 {
		return false
	}
	for _, entry := range m.Entries {
		if entry.SHA256 == "" {
			return false
		}
	}
	return true
}

func (s *Store) chooseVersion(name string, src source.Model, requested string, files []manifest.File, bytes int64) (string, bool, error) {
	if src.Revision == "" {
		return "", false, fmt.Errorf("immutable source revision is required")
	}
	if requested != "" {
		if !validVersionPrefix(requested) || !strings.HasPrefix(src.Revision, requested) {
			return "", false, fmt.Errorf("invalid requested version %q for revision %q", requested, src.Revision)
		}
		return s.checkVersionPath(name, requested, src, files, bytes)
	}
	start := min(12, len(src.Revision))
	for n := start; n <= len(src.Revision); n = min(n+4, len(src.Revision)) {
		version := src.Revision[:n]
		_, existing, err := s.checkVersionPath(name, version, src, files, bytes)
		if err == nil {
			return version, existing, nil
		}
		var collision versionCollisionError
		if !errors.As(err, &collision) {
			return "", false, err
		}
		if n == len(src.Revision) {
			break
		}
	}
	return "", false, fmt.Errorf("full revision collision for %s", src.Revision)
}

func validVersionPrefix(version string) bool {
	return version != "" && version != "." && version != ".." && version != "current" &&
		!strings.ContainsAny(version, `/\\`) && !strings.ContainsFunc(version, unicode.IsControl)
}

type versionCollisionError struct{ version string }

func (e versionCollisionError) Error() string { return "version collision: " + e.version }

func (s *Store) checkVersionPath(name, version string, src source.Model, files []manifest.File, bytes int64) (string, bool, error) {
	dir := filepath.Join(s.Root, name, version)
	m, err := manifest.Read(dir)
	if os.IsNotExist(err) {
		return version, false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("existing version has invalid manifest: %s", dir)
	}
	want := manifest.Manifest{Source: manifest.Source{Provider: src.Provider, Repo: src.Repo, Revision: src.Revision}}
	if m.IdentityEqual(want) {
		if m.Files != len(files) || m.Bytes != bytes {
			return "", false, fmt.Errorf("existing immutable version differs from source")
		}
		return version, true, nil
	}
	return "", false, versionCollisionError{version}
}

func (s *Store) checkNameIdentity(name string, src source.Model) error {
	versions, err := s.List(name)
	if err != nil {
		return err
	}
	for _, v := range versions {
		if v.Manifest.Source.Provider != src.Provider || v.Manifest.Source.Repo != src.Repo {
			return fmt.Errorf("model name %q is already used by %s:%s; use --name", name, v.Manifest.Source.Provider, v.Manifest.Source.Repo)
		}
	}
	return nil
}

func (s *Store) List(model string) ([]Version, error) {
	entries, err := os.ReadDir(s.Root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Version
	for _, modelDir := range entries {
		name := modelDir.Name()
		if !modelDir.IsDir() || strings.HasPrefix(name, ".") || (model != "" && name != model) {
			continue
		}
		current := ""
		if link, err := os.Readlink(filepath.Join(s.Root, name, "current")); err == nil {
			current = filepath.Base(link)
		}
		versions, err := os.ReadDir(filepath.Join(s.Root, name))
		if err != nil {
			return nil, err
		}
		for _, versionDir := range versions {
			if !versionDir.IsDir() || strings.HasPrefix(versionDir.Name(), ".") {
				continue
			}
			dir := filepath.Join(s.Root, name, versionDir.Name())
			m, err := manifest.Read(dir)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", dir, err)
			}
			out = append(out, Version{Name: name, Version: versionDir.Name(), Path: dir, Current: versionDir.Name() == current, Manifest: m})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].Version < out[j].Version
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *Store) Resolve(ref string) (Version, error) {
	name, ver := splitRef(ref)
	if err := naming.Validate(name); err != nil {
		return Version{}, err
	}
	current := ver == ""
	if ver == "" {
		link, err := os.Readlink(filepath.Join(s.Root, name, "current"))
		if err != nil {
			return Version{}, fmt.Errorf("model %q has no valid current version", name)
		}
		ver = filepath.Base(link)
	}
	dir := filepath.Join(s.Root, name, ver)
	physical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return Version{}, err
	}
	if filepath.Dir(physical) != filepath.Join(s.Root, name) {
		return Version{}, fmt.Errorf("version resolves outside model directory")
	}
	m, err := manifest.Read(physical)
	if err != nil {
		return Version{}, err
	}
	if !current {
		link, err := os.Readlink(filepath.Join(s.Root, name, "current"))
		current = err == nil && filepath.Base(link) == ver
	}
	return Version{Name: name, Version: ver, Path: physical, Current: current, Manifest: m}, nil
}

func (s *Store) Activate(ref string, noVerify bool) error {
	name, version := splitRef(ref)
	if version == "" {
		return fmt.Errorf("activate requires model@version")
	}
	if err := s.Init(); err != nil {
		return err
	}
	l, err := lock.Acquire(s.lockDir(), name, 30*time.Second)
	if err != nil {
		return err
	}
	defer l.Release()
	return s.activateLocked(name, version, !noVerify)
}

func (s *Store) activateLocked(name, version string, verify bool) error {
	dir := filepath.Join(s.Root, name, version)
	if verify {
		if _, err := s.Verify(name+"@"+version, false, false); err != nil {
			return err
		}
	} else if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("version not found: %s@%s", name, version)
	}
	modelDir := filepath.Join(s.Root, name)
	tmp := filepath.Join(modelDir, ".current.part")
	_ = os.Remove(tmp)
	if err := os.Symlink(version, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(modelDir, "current")); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return fsutil.SyncDir(modelDir)
}

func (s *Store) Verify(ref string, full, record bool) (manifest.Manifest, error) {
	if record && !full {
		return manifest.Manifest{}, fmt.Errorf("--record requires --full")
	}
	v, err := s.Resolve(ref)
	if err != nil {
		return manifest.Manifest{}, err
	}
	files, bytes, err := fsutil.Scan(v.Path, full)
	if err != nil {
		return manifest.Manifest{}, err
	}
	if len(files) != v.Manifest.Files || bytes != v.Manifest.Bytes {
		return manifest.Manifest{}, fmt.Errorf("quick verification failed: expected %d files/%d bytes, got %d/%d", v.Manifest.Files, v.Manifest.Bytes, len(files), bytes)
	}
	if full && len(v.Manifest.Entries) > 0 && !fsutil.SameTree(files, v.Manifest.Entries, true) {
		return manifest.Manifest{}, fmt.Errorf("full verification failed")
	}
	if record {
		v.Manifest.Entries = files
		if err := manifest.Write(v.Path, v.Manifest); err != nil {
			return manifest.Manifest{}, err
		}
	}
	return v.Manifest, nil
}

func (s *Store) Rename(oldName, newName string, dryRun bool) error {
	if err := naming.Validate(oldName); err != nil {
		return err
	}
	if err := naming.Validate(newName); err != nil {
		return err
	}
	oldPath, newPath := filepath.Join(s.Root, oldName), filepath.Join(s.Root, newName)
	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("target model already exists: %s", newName)
	}
	if dryRun {
		return nil
	}
	if err := s.Init(); err != nil {
		return err
	}
	l, err := lock.Acquire(s.lockDir(), oldName, 30*time.Second)
	if err != nil {
		return err
	}
	defer l.Release()
	if err := os.Rename(oldPath, newPath); err != nil {
		return err
	}
	versions, err := s.List(newName)
	if err != nil {
		return err
	}
	for _, v := range versions {
		v.Manifest.Name = newName
		if err := manifest.Write(v.Path, v.Manifest); err != nil {
			return err
		}
	}
	return fsutil.SyncDir(s.Root)
}

func (s *Store) Remove(ref string, inactive, allVersions, force, dryRun bool) ([]string, error) {
	name, version := splitRef(ref)
	if version == "" && !inactive && !allVersions {
		return nil, fmt.Errorf("remove requires model@version unless --inactive or --all-versions is used")
	}
	versions, err := s.List(name)
	if err != nil {
		return nil, err
	}
	var targets []Version
	for _, v := range versions {
		if version != "" && v.Version != version {
			continue
		}
		if inactive && v.Current {
			continue
		}
		if v.Current && !force {
			return nil, fmt.Errorf("refusing to remove current version %s@%s (use --force)", name, v.Version)
		}
		targets = append(targets, v)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no matching versions found for %s", ref)
	}
	if !dryRun {
		for _, v := range targets {
			if err := os.RemoveAll(v.Path); err != nil {
				return nil, err
			}
			if v.Current {
				_ = os.Remove(filepath.Join(s.Root, name, "current"))
			}
		}
		modelDir := filepath.Join(s.Root, name)
		if entries, readErr := os.ReadDir(modelDir); readErr == nil && len(entries) == 0 {
			_ = os.Remove(modelDir)
		}
	}
	var removed []string
	for _, v := range targets {
		removed = append(removed, v.Name+"@"+v.Version)
	}
	return removed, nil
}

func (s *Store) CopyTo(dst *Store, refs []string, allVersions, currentOnly, full, dryRun bool) ([]ImportResult, error) {
	versions, err := s.List("")
	if err != nil {
		return nil, err
	}
	selected := map[string]bool{}
	for _, r := range refs {
		selected[r] = true
	}
	var out []ImportResult
	for _, v := range versions {
		if currentOnly && !v.Current {
			continue
		}
		if len(selected) > 0 && !selected[v.Name] && !selected[v.Name+"@"+v.Version] {
			continue
		}
		exact := selected[v.Name+"@"+v.Version]
		if !allVersions && !currentOnly && !exact && !v.Current {
			continue
		}
		res, err := dst.Import(source.Model{
			Provider: v.Manifest.Source.Provider, Repo: v.Manifest.Source.Repo,
			Revision: v.Manifest.Source.Revision, Path: v.Path, Status: "ready",
		}, ImportOptions{Name: v.Name, Version: v.Version, Activate: v.Current, Hash: full, DryRun: dryRun})
		if err != nil {
			return out, err
		}
		out = append(out, res)
	}
	return out, nil
}

func (s *Store) GC(olderThan time.Duration, dryRun bool) ([]string, error) {
	cutoff := time.Now().Add(-olderThan)
	var removed []string
	for _, root := range []string{s.stageDir(), s.lockDir()} {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || path == root {
				return nil
			}
			info, statErr := d.Info()
			if statErr == nil && info.ModTime().Before(cutoff) &&
				(d.IsDir() || strings.HasSuffix(path, ".part") || strings.HasSuffix(path, ".lock")) {
				removed = append(removed, path)
				if !dryRun {
					if d.IsDir() {
						return os.RemoveAll(path)
					}
					return os.Remove(path)
				}
			}
			return nil
		})
	}
	return removed, nil
}

type DoctorResult struct {
	Check  string `json:"check"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

func (s *Store) Doctor(writeTest bool) []DoctorResult {
	var out []DoctorResult
	err := s.Init()
	out = append(out, DoctorResult{"store", err == nil, errorText(err)})
	if err != nil {
		return out
	}
	var stat syscall.Statfs_t
	err = syscall.Statfs(s.Root, &stat)
	detail := ""
	if err == nil {
		detail = fmt.Sprintf("%d bytes available", stat.Bavail*uint64(stat.Bsize))
	}
	out = append(out, DoctorResult{"disk", err == nil && stat.Bavail > 0, detail})
	versions, listErr := s.List("")
	out = append(out, DoctorResult{"manifests", listErr == nil, errorText(listErr)})
	broken := 0
	models := map[string]bool{}
	for _, v := range versions {
		models[v.Name] = true
	}
	for name := range models {
		link := filepath.Join(s.Root, name, "current")
		if _, err := os.Lstat(link); err == nil {
			if _, err := s.Resolve(name); err != nil {
				broken++
			}
		}
	}
	out = append(out, DoctorResult{"current-links", broken == 0, fmt.Sprintf("%d broken or missing", broken)})
	if writeTest {
		p := filepath.Join(s.stageDir(), ".doctor.part")
		err := os.WriteFile(p, []byte("mstore"), 0o600)
		if err == nil {
			err = os.Rename(p, filepath.Join(s.stageDir(), ".doctor"))
			_ = os.Remove(filepath.Join(s.stageDir(), ".doctor"))
		}
		out = append(out, DoctorResult{"atomic-write", err == nil, errorText(err)})
		link := filepath.Join(s.stageDir(), ".doctor-link")
		_ = os.Remove(link)
		err = os.Symlink("relative-target", link)
		if err == nil {
			target, readErr := os.Readlink(link)
			if readErr != nil || target != "relative-target" {
				err = fmt.Errorf("relative symlink validation failed")
			}
			_ = os.Remove(link)
		}
		out = append(out, DoctorResult{"relative-symlink", err == nil, errorText(err)})
	}
	return out
}

func splitRef(ref string) (string, string) {
	if i := strings.LastIndexByte(ref, '@'); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

func stageKey(name string, src source.Model) string {
	h := sha256.Sum256([]byte(src.Provider + "\x00" + src.Repo + "\x00" + src.Revision))
	return name + "-" + hex.EncodeToString(h[:8])
}

func (s *Store) stageDir() string { return filepath.Join(s.Root, ".stage") }
func (s *Store) lockDir() string  { return filepath.Join(s.Root, ".locks") }

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

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func ensureFreeSpace(path string, required int64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return err
	}
	available := stat.Bavail * uint64(stat.Bsize)
	if required > 0 && uint64(required) > available {
		return fmt.Errorf("no space left: need %d bytes, have %d", required, available)
	}
	return nil
}

func JSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

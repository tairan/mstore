package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chieworks/mstore/internal/lock"
	"github.com/chieworks/mstore/internal/naming"
	"github.com/chieworks/mstore/internal/providers"
	"github.com/chieworks/mstore/internal/source"
	"github.com/chieworks/mstore/internal/source/huggingface"
	"github.com/chieworks/mstore/internal/source/modelscope"
	"github.com/chieworks/mstore/internal/store"
)

var pruneStatuses = []string{"incomplete", "invalid", "conflict"}

type pruneItem struct {
	Provider string `json:"provider"`
	Repo     string `json:"repo"`
	Revision string `json:"revision,omitempty"`
	Status   string `json:"status"`
	Action   string `json:"action"`
	Reason   string `json:"reason"`
	Path     string `json:"path,omitempty"`
	Error    string `json:"error,omitempty"`
}

type pruneSummary struct {
	Keep   int `json:"keep"`
	Delete int `json:"delete"`
	Skip   int `json:"skip"`
	Failed int `json:"failed"`
}

type pruneReport struct {
	DryRun  bool         `json:"dry_run"`
	Items   []pruneItem  `json:"items"`
	Errors  []string     `json:"errors,omitempty"`
	Summary pruneSummary `json:"summary"`
}

type pruneModel struct {
	model         source.Model
	reason        string
	ambiguous     bool
	providerReady bool
	owner         bool
	ownerReason   string
}

func (a *app) prune(args []string) error {
	f := newFlags("prune")
	provider := f.String("provider", "all", "")
	status := f.String("status", strings.Join(pruneStatuses, ","), "")
	dryRun := f.Bool("dry-run", false, "")
	yes := f.Bool("yes", false, "")
	force := f.Bool("force", false, "")
	jsonOutput := f.Bool("json", false, "")
	if err := f.Parse(args); err != nil {
		return usageError("%v", err)
	}
	if len(f.Args()) != 0 {
		return usageError("prune accepts no positional arguments")
	}
	if err := validProvider(*provider); err != nil {
		return err
	}
	statuses, err := parsePruneStatuses(*status)
	if err != nil {
		return err
	}

	report, runErr := buildPruneReport(a.store, *provider, statuses, *force)
	report.DryRun = !*yes || *dryRun
	if runErr == nil && *yes && !*dryRun {
		runErr = executePrune(a.store, &report, *provider, statuses, *force)
	}
	report.Summary = summarizePrune(report.Items)
	if a.global.json || *jsonOutput {
		if err := writeJSON(a.out, report); err != nil {
			return err
		}
	} else {
		printPruneReport(a.out, report)
	}
	return runErr
}

func parsePruneStatuses(raw string) (map[string]bool, error) {
	allowed := map[string]bool{}
	for _, status := range strings.Split(raw, ",") {
		status = strings.TrimSpace(status)
		if status == "" || !containsString(pruneStatuses, status) {
			return nil, usageError("--status must contain only incomplete, invalid, or conflict")
		}
		allowed[status] = true
	}
	if len(allowed) == 0 {
		return nil, usageError("--status must not be empty")
	}
	return allowed, nil
}

func buildPruneReport(s *store.Store, provider string, statuses map[string]bool, force bool) (pruneReport, error) {
	var report pruneReport
	versions, err := s.List("")
	if err != nil {
		return report, err
	}
	if err := validatePruneRoots(s, provider); err != nil {
		return report, err
	}
	models, scanErrs := providers.Scan(provider)
	for _, scanErr := range scanErrs {
		if errors.Is(scanErr, os.ErrNotExist) {
			continue
		}
		report.Errors = append(report.Errors, scanErr.Error())
	}
	if len(report.Errors) > 0 {
		return report, fmt.Errorf("prune provider scan failed: %s", strings.Join(report.Errors, "; "))
	}

	classified, _ := classifyPruneModels(models, versions)
	for _, item := range classified {
		if item.owner {
			if item.ownerReason == "" {
				item.ownerReason = "imported in mstore"
			}
			report.Items = append(report.Items, pruneItemFromModel(item.model, "keep", item.ownerReason))
			continue
		}
		if !statuses[item.model.Status] {
			continue
		}
		action, reason := "delete", item.reason
		if item.ambiguous {
			action, reason = "skip", "multiple unimported sources use the same default name"
		}
		if item.providerReady {
			action, reason = "skip", "protected: provider source is ready"
		} else if item.model.Preferred && !force {
			action, reason = "skip", "protected: provider current revision"
		}
		pruneItem := pruneItemFromModel(item.model, action, reason)
		if pruneItem.Action == "delete" {
			if err := validatePruneTarget(s, pruneItem); err != nil {
				pruneItem.Action = "skip"
				pruneItem.Reason = err.Error()
			}
		}
		report.Items = append(report.Items, pruneItem)
	}
	sort.SliceStable(report.Items, func(i, j int) bool {
		return pruneItemLess(report.Items[i], report.Items[j])
	})
	return report, nil
}

func classifyPruneModels(models []source.Model, versions []store.Version) ([]pruneModel, map[string][]store.Version) {
	imported := map[string]bool{}
	importedRepos := map[string]bool{}
	owners := map[string][]store.Version{}
	for _, version := range versions {
		imported[identityKey(version.Manifest.Source.Provider, version.Manifest.Source.Repo, version.Manifest.Source.Revision)] = true
		importedRepos[sourceKey(version.Manifest.Source.Provider, version.Manifest.Source.Repo)] = true
		owners[version.Name] = append(owners[version.Name], version)
	}

	var classified []pruneModel
	byName := map[string]map[string][]int{}
	for i := range models {
		model := models[i]
		providerReady := model.Status == "ready"
		if imported[identityKey(model.Provider, model.Repo, model.Revision)] {
			continue
		}
		if model.Revision == "" && importedRepos[sourceKey(model.Provider, model.Repo)] {
			classified = append(classified, pruneModel{model: model, owner: true, ownerReason: "imported in mstore"})
			continue
		}
		if model.Status == "ready" {
			name, err := naming.Normalize(model.Repo)
			if err != nil {
				model.Status, model.Error = "invalid", err.Error()
			} else {
				ownerConflict := false
				for _, version := range owners[name] {
					if sourceKey(version.Manifest.Source.Provider, version.Manifest.Source.Repo) != sourceKey(model.Provider, model.Repo) {
						ownerConflict = true
						break
					}
				}
				if ownerConflict {
					model.Status = "conflict"
					model.Error = "default name is already used by an imported source"
				}
			}
		}
		item := pruneModel{model: model, reason: model.Error, providerReady: providerReady}
		if model.Status == "conflict" && item.reason == "" {
			item.reason = "default name is already used"
		}
		classified = append(classified, item)
		if model.Status == "ready" {
			if name, err := naming.Normalize(model.Repo); err == nil {
				key := sourceKey(model.Provider, model.Repo)
				if byName[name] == nil {
					byName[name] = map[string][]int{}
				}
				byName[name][key] = append(byName[name][key], len(classified)-1)
			}
		}
	}
	for _, groups := range byName {
		if len(groups) < 2 {
			continue
		}
		for _, indexes := range groups {
			for _, index := range indexes {
				classified[index].model.Status = "conflict"
				classified[index].ambiguous = true
				classified[index].reason = "multiple unimported sources use the same default name"
			}
		}
	}
	ownerSeen := map[string]bool{}
	for _, item := range classified {
		if item.model.Status != "conflict" {
			continue
		}
		name, err := naming.Normalize(item.model.Repo)
		if err != nil {
			continue
		}
		for _, version := range owners[name] {
			if sourceKey(version.Manifest.Source.Provider, version.Manifest.Source.Repo) == sourceKey(item.model.Provider, item.model.Repo) {
				continue
			}
			ownerKey := identityKey(version.Manifest.Source.Provider, version.Manifest.Source.Repo, version.Manifest.Source.Revision) + "\x00" + version.Path
			if ownerSeen[ownerKey] {
				continue
			}
			ownerSeen[ownerKey] = true
			classified = append(classified, pruneModel{model: source.Model{
				Provider: version.Manifest.Source.Provider,
				Repo:     version.Manifest.Source.Repo,
				Revision: version.Manifest.Source.Revision,
				Path:     version.Path,
				Status:   "imported",
			}, owner: true, ownerReason: "imported in mstore"})
		}
	}
	return classified, owners
}

func executePrune(s *store.Store, report *pruneReport, provider string, statuses map[string]bool, force bool) error {
	var failures []string
	for i := range report.Items {
		item := &report.Items[i]
		if item.Action != "delete" {
			continue
		}
		current, err := buildPruneReport(s, providerForItem(provider, item), statuses, force)
		if err != nil {
			item.Action, item.Error = "failed", err.Error()
			failures = append(failures, err.Error())
			continue
		}
		if !hasMatchingDelete(current.Items, *item) {
			item.Action = "skip"
			item.Reason = "target changed since scan"
			continue
		}
		if err := validatePruneTarget(s, *item); err != nil {
			item.Action, item.Error = "failed", err.Error()
			failures = append(failures, err.Error())
			continue
		}
		if err := removePruneTarget(*item); err != nil {
			item.Action, item.Error = "failed", fmt.Errorf("remove %s: %w", item.Path, err).Error()
			failures = append(failures, item.Error)
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("prune deletion failed: %s", strings.Join(failures, "; "))
	}
	return nil
}

func providerForItem(provider string, item *pruneItem) string {
	if provider == "all" {
		return item.Provider
	}
	return provider
}

func hasMatchingDelete(items []pruneItem, want pruneItem) bool {
	for _, item := range items {
		if item.Action == "delete" && item.Provider == want.Provider && item.Repo == want.Repo && item.Revision == want.Revision && item.Status == want.Status && item.Path == want.Path {
			return true
		}
	}
	return false
}

func validatePruneTarget(s *store.Store, item pruneItem) error {
	root, err := providerCacheRoot(item.Provider)
	if err != nil {
		return err
	}
	root, err = filepath.Abs(filepath.Clean(root))
	if err != nil {
		return err
	}
	path, err := filepath.Abs(filepath.Clean(item.Path))
	if err != nil {
		return err
	}
	if path == root {
		return fmt.Errorf("refusing to remove provider cache root: %s", path)
	}
	expected, err := expectedPruneTarget(root, item)
	if err != nil {
		return err
	}
	if path != expected {
		return fmt.Errorf("refusing to remove imprecise provider target: %s", path)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing to remove path outside provider cache: %s", path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect prune target %s: %w", path, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("refusing to remove non-directory or symlink: %s", path)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	rel, err = filepath.Rel(canonicalRoot, canonicalPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing to remove symlinked path outside provider cache: %s", path)
	}
	if locked, err := hasPruneLock(path); err != nil {
		return fmt.Errorf("inspect target locks %s: %w", path, err)
	} else if locked {
		return fmt.Errorf("target is locked: %s", path)
	}
	if locked, err := hasProviderLock(root, item); err != nil {
		return fmt.Errorf("inspect provider locks %s: %w", path, err)
	} else if locked {
		return fmt.Errorf("target is locked: %s", path)
	}
	return nil
}

func validatePruneRoots(s *store.Store, provider string) error {
	providersToCheck := []string{provider}
	if provider == "all" {
		providersToCheck = []string{"hf", "ms"}
	}
	storeRoot, err := filepath.Abs(filepath.Clean(s.Root))
	if err != nil {
		return err
	}
	for _, name := range providersToCheck {
		cacheRoot, rootErr := providerCacheRoot(name)
		if rootErr != nil {
			return rootErr
		}
		cacheRoot, rootErr = filepath.Abs(filepath.Clean(cacheRoot))
		if rootErr != nil {
			return rootErr
		}
		if pathsOverlap(storeRoot, cacheRoot) {
			return fmt.Errorf("refusing to prune: store root overlaps %s provider cache root", name)
		}
	}
	return nil
}

func pathsOverlap(a, b string) bool {
	inside := func(root, path string) bool {
		rel, err := filepath.Rel(root, path)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return inside(a, b) || inside(b, a)
}

func expectedPruneTarget(root string, item pruneItem) (string, error) {
	if item.Provider == "ms" {
		return modelscope.RepoPath(root, item.Repo)
	}
	if item.Provider != "hf" {
		return "", fmt.Errorf("unsupported provider %q", item.Provider)
	}
	repoPath := filepath.Join(root, "models--"+strings.ReplaceAll(item.Repo, "/", "--"))
	if item.Status == "incomplete" {
		return repoPath, nil
	}
	if item.Revision == "" || strings.ContainsAny(item.Revision, `/\\`) {
		return "", fmt.Errorf("refusing to remove target with invalid revision")
	}
	return filepath.Join(repoPath, "snapshots", item.Revision), nil
}

func hasProviderLock(root string, item pruneItem) (bool, error) {
	if item.Provider != "hf" {
		return false, nil
	}
	lockRoot := filepath.Join(root, ".locks", "models--"+strings.ReplaceAll(item.Repo, "/", "--"))
	locked := false
	err := filepath.WalkDir(lockRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(entry.Name(), ".lock") {
			active, activeErr := lock.Active(path)
			if activeErr != nil {
				return activeErr
			}
			if active {
				locked = true
				return fs.SkipDir
			}
		}
		return nil
	})
	if os.IsNotExist(err) {
		return false, nil
	}
	return locked, err
}

func hasPruneLock(path string) (bool, error) {
	locked := false
	err := filepath.WalkDir(path, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if current != path && strings.HasSuffix(entry.Name(), ".lock") {
			active, activeErr := lock.Active(current)
			if activeErr != nil {
				return activeErr
			}
			if active {
				locked = true
				if entry.IsDir() {
					return fs.SkipDir
				}
			}
		}
		return nil
	})
	return locked, err
}

func removePruneTarget(item pruneItem) (err error) {
	root, err := providerCacheRoot(item.Provider)
	if err != nil {
		return err
	}
	path := item.Path
	parent := filepath.Dir(path)
	claim := filepath.Join(parent, ".mstore-prune-"+fmt.Sprintf("%d", os.Getpid()))
	for i := 0; ; i++ {
		if i > 0 {
			claim = filepath.Join(parent, fmt.Sprintf(".mstore-prune-%d-%d", os.Getpid(), i))
		}
		if _, statErr := os.Lstat(claim); os.IsNotExist(statErr) {
			break
		} else if statErr != nil {
			return statErr
		}
	}
	if err := os.Rename(path, claim); err != nil {
		return err
	}
	if locked, lockErr := hasProviderLock(root, item); lockErr != nil {
		return fmt.Errorf("%w (claimed data remains at %s)", lockErr, claim)
	} else if locked {
		return fmt.Errorf("target became locked during deletion (claimed data remains at %s)", claim)
	}
	if locked, lockErr := hasPruneLock(claim); lockErr != nil {
		return fmt.Errorf("%w (claimed data remains at %s)", lockErr, claim)
	} else if locked {
		return fmt.Errorf("target became locked during deletion (claimed data remains at %s)", claim)
	}
	if err := os.RemoveAll(claim); err != nil {
		return fmt.Errorf("remove claimed data %s: %w", claim, err)
	}
	return nil
}

func providerCacheRoot(provider string) (string, error) {
	switch provider {
	case "hf":
		return huggingface.CacheRoot()
	case "ms":
		return modelscope.CacheRoot()
	default:
		return "", fmt.Errorf("unsupported provider %q", provider)
	}
}

func pruneItemFromModel(model source.Model, action, reason string) pruneItem {
	return pruneItem{Provider: model.Provider, Repo: model.Repo, Revision: model.Revision,
		Status: model.Status, Action: action, Reason: reason, Path: model.Path}
}

func summarizePrune(items []pruneItem) pruneSummary {
	var summary pruneSummary
	for _, item := range items {
		switch item.Action {
		case "keep":
			summary.Keep++
		case "delete":
			summary.Delete++
		case "skip":
			summary.Skip++
		case "failed":
			summary.Failed++
		}
	}
	return summary
}

func printPruneReport(w interface{ Write([]byte) (int, error) }, report pruneReport) {
	for _, item := range report.Items {
		fmt.Fprintf(w, "%-6s %-10s %s:%s@%s", strings.ToUpper(item.Action), item.Status, item.Provider, item.Repo, item.Revision)
		if item.Reason != "" {
			fmt.Fprintf(w, " %s", item.Reason)
		}
		if item.Error != "" {
			fmt.Fprintf(w, ": %s", item.Error)
		}
		fmt.Fprintln(w)
	}
}

func pruneItemLess(a, b pruneItem) bool {
	left := a.Provider + "\x00" + a.Repo + "\x00" + a.Revision + "\x00" + a.Action + "\x00" + a.Path
	right := b.Provider + "\x00" + b.Repo + "\x00" + b.Revision + "\x00" + b.Action + "\x00" + b.Path
	return left < right
}

func identityKey(provider, repo, revision string) string {
	return provider + "\x00" + repo + "\x00" + revision
}

func sourceKey(provider, repo string) string { return provider + "\x00" + repo }

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

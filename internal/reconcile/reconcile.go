package reconcile

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/chieworks/mstore/internal/naming"
	"github.com/chieworks/mstore/internal/providers"
	"github.com/chieworks/mstore/internal/source"
	"github.com/chieworks/mstore/internal/store"
)

type Repository interface {
	List(model string) ([]store.Version, error)
	Import(src source.Model, opts store.ImportOptions) (store.ImportResult, error)
	Activate(ref string, noVerify bool) error
}

type Scanner func(provider string) ([]source.Model, []error)

type Options struct {
	Provider string
	Models   []string
	Activate bool
	Hash     bool
	Jobs     int
	DryRun   bool
}

type Item struct {
	Operation string `json:"operation"`
	Status    string `json:"status"`
	Source    string `json:"source,omitempty"`
	Name      string `json:"name,omitempty"`
	Version   string `json:"version,omitempty"`
	Path      string `json:"path,omitempty"`
	Error     string `json:"error,omitempty"`
}

type Summary struct {
	Planned   int `json:"planned"`
	Imported  int `json:"imported"`
	Skipped   int `json:"skipped"`
	Activated int `json:"activated"`
	Conflict  int `json:"conflict"`
	Failed    int `json:"failed"`
}

type Report struct {
	Results []Item  `json:"results"`
	Summary Summary `json:"summary"`
}

type SelectionError struct{ Message string }

func (e SelectionError) Error() string { return e.Message }

type RunError struct {
	Failed    int
	Conflicts int
	Cause     error
}

func (e *RunError) Error() string {
	switch {
	case e.Failed > 0 && e.Cause != nil:
		return fmt.Sprintf("sync completed with %d failed and %d conflict: %v", e.Failed, e.Conflicts, e.Cause)
	case e.Failed > 0:
		return fmt.Sprintf("sync completed with %d failed and %d conflict", e.Failed, e.Conflicts)
	default:
		return fmt.Sprintf("sync completed with %d conflict", e.Conflicts)
	}
}

func (e *RunError) Unwrap() error { return e.Cause }

type repoKey struct {
	provider string
	repo     string
}

type identity struct {
	repoKey
	revision string
}

type candidate struct {
	model   source.Model
	key     repoKey
	name    string
	version string
}

type importOutcome struct {
	candidate candidate
	item      Item
	version   string
	err       error
}

func Run(repo Repository, scan Scanner, opts Options) (Report, error) {
	var report Report
	if opts.Provider != "all" && opts.Provider != "hf" && opts.Provider != "ms" {
		return report, SelectionError{Message: "--provider must be all, hf, or ms"}
	}
	if opts.Jobs < 1 {
		return report, SelectionError{Message: "--jobs must be at least 1"}
	}
	existing, err := repo.List("")
	if err != nil {
		return report, err
	}

	namesByRepo := map[repoKey]string{}
	reposByName := map[string]repoKey{}
	versionsByIdentity := map[identity]string{}
	for _, version := range existing {
		key := repoKey{provider: version.Manifest.Source.Provider, repo: version.Manifest.Source.Repo}
		if prior, ok := namesByRepo[key]; ok && prior != version.Name {
			return report, fmt.Errorf("source %s:%s is registered under multiple names", key.provider, key.repo)
		}
		if prior, ok := reposByName[version.Name]; ok && prior != key {
			return report, fmt.Errorf("model name %q contains multiple source identities", version.Name)
		}
		namesByRepo[key] = version.Name
		reposByName[version.Name] = key
		versionsByIdentity[identity{repoKey: key, revision: version.Manifest.Source.Revision}] = version.Version
	}

	requested := make(map[string]bool, len(opts.Models))
	selectedRepos := map[repoKey]bool{}
	for _, name := range opts.Models {
		requested[name] = true
		key, ok := reposByName[name]
		if !ok {
			return report, SelectionError{Message: fmt.Sprintf("model %q is not registered", name)}
		}
		if opts.Provider != "all" && key.provider != opts.Provider {
			return report, SelectionError{Message: fmt.Sprintf("model %q uses provider %s, excluded by --provider %s", name, key.provider, opts.Provider)}
		}
		selectedRepos[key] = true
	}

	models, scanErrs := scan(opts.Provider)
	var firstFailure error
	for _, scanErr := range scanErrs {
		var providerErr providers.ScanError
		provider := ""
		if errors.As(scanErr, &providerErr) {
			provider = providerErr.Provider
		}
		if errors.Is(scanErr, os.ErrNotExist) {
			report.Results = append(report.Results, Item{
				Operation: "scan", Status: "skipped", Source: provider,
				Error: "provider cache not found",
			})
			continue
		}
		report.Results = append(report.Results, Item{
			Operation: "scan", Status: "failed", Source: provider, Error: scanErr.Error(),
		})
		if firstFailure == nil {
			firstFailure = scanErr
		}
	}

	sort.Slice(models, func(i, j int) bool { return models[i].Ref() < models[j].Ref() })
	seenIdentity := map[identity]bool{}
	var candidates []candidate
	for _, model := range models {
		if model.Status != "ready" || (opts.Provider != "all" && model.Provider != opts.Provider) {
			continue
		}
		key := repoKey{provider: model.Provider, repo: model.Repo}
		if len(requested) > 0 && !selectedRepos[key] {
			continue
		}
		id := identity{repoKey: key, revision: model.Revision}
		if seenIdentity[id] {
			continue
		}
		seenIdentity[id] = true
		name := namesByRepo[key]
		if name == "" {
			name, err = naming.Normalize(model.Repo)
			if err != nil {
				report.Results = append(report.Results, Item{
					Operation: "import", Status: "failed", Source: model.Ref(), Error: err.Error(),
				})
				if firstFailure == nil {
					firstFailure = err
				}
				continue
			}
		}
		candidates = append(candidates, candidate{
			model: model, key: key, name: name, version: versionsByIdentity[id],
		})
	}

	conflicted := preflightConflicts(candidates, reposByName)
	repoCandidates := map[repoKey][]candidate{}
	var imports []candidate
	for _, candidate := range candidates {
		repoCandidates[candidate.key] = append(repoCandidates[candidate.key], candidate)
		if message := conflicted[candidate.key]; message != "" {
			report.Results = append(report.Results, Item{
				Operation: "import", Status: "conflict", Source: candidate.model.Ref(),
				Name: candidate.name, Error: message,
			})
			continue
		}
		if candidate.version != "" {
			report.Results = append(report.Results, Item{
				Operation: "import", Status: "skipped", Source: candidate.model.Ref(),
				Name: candidate.name, Version: candidate.version,
				Path: versionPath(existing, candidate),
			})
			continue
		}
		imports = append(imports, candidate)
	}

	outcomes := importAll(repo, imports, opts)
	for _, outcome := range outcomes {
		report.Results = append(report.Results, outcome.item)
		if outcome.err != nil {
			if firstFailure == nil {
				firstFailure = outcome.err
			}
			continue
		}
		versionsByIdentity[identity{repoKey: outcome.candidate.key, revision: outcome.candidate.model.Revision}] = outcome.version
	}

	if opts.Activate {
		keys := make([]repoKey, 0, len(repoCandidates))
		for key := range repoCandidates {
			if conflicted[key] == "" {
				keys = append(keys, key)
			}
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].provider == keys[j].provider {
				return keys[i].repo < keys[j].repo
			}
			return keys[i].provider < keys[j].provider
		})
		for _, key := range keys {
			item, activateErr := activate(repo, key, repoCandidates[key], versionsByIdentity, opts.DryRun)
			report.Results = append(report.Results, item)
			if activateErr != nil && firstFailure == nil {
				firstFailure = activateErr
			}
		}
	}

	sort.SliceStable(report.Results, func(i, j int) bool {
		if report.Results[i].Source == report.Results[j].Source {
			return operationOrder(report.Results[i].Operation) < operationOrder(report.Results[j].Operation)
		}
		return report.Results[i].Source < report.Results[j].Source
	})
	report.Summary = summarize(report.Results)
	if report.Summary.Failed > 0 || report.Summary.Conflict > 0 {
		return report, &RunError{
			Failed: report.Summary.Failed, Conflicts: report.Summary.Conflict, Cause: firstFailure,
		}
	}
	return report, nil
}

func preflightConflicts(candidates []candidate, existing map[string]repoKey) map[repoKey]string {
	conflicted := map[repoKey]string{}
	keysByName := map[string]map[repoKey]bool{}
	for _, candidate := range candidates {
		if owner, ok := existing[candidate.name]; ok {
			if owner != candidate.key {
				conflicted[candidate.key] = fmt.Sprintf(
					"model name %q is already used by %s:%s; import explicitly with --name",
					candidate.name, owner.provider, owner.repo,
				)
			}
			continue
		}
		if keysByName[candidate.name] == nil {
			keysByName[candidate.name] = map[repoKey]bool{}
		}
		keysByName[candidate.name][candidate.key] = true
	}
	for name, keys := range keysByName {
		if len(keys) < 2 {
			continue
		}
		for key := range keys {
			conflicted[key] = fmt.Sprintf(
				"multiple new repositories normalize to %q; import each explicitly with --name", name,
			)
		}
	}
	return conflicted
}

func importAll(repo Repository, candidates []candidate, opts Options) []importOutcome {
	if len(candidates) == 0 {
		return nil
	}
	jobs := min(opts.Jobs, len(candidates))
	input := make(chan candidate)
	output := make(chan importOutcome, len(candidates))
	var workers sync.WaitGroup
	for range jobs {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for candidate := range input {
				result, err := repo.Import(candidate.model, store.ImportOptions{
					Name: candidate.name, Hash: opts.Hash, DryRun: opts.DryRun,
				})
				item := Item{
					Operation: "import", Source: candidate.model.Ref(), Name: candidate.name,
					Version: result.Version, Path: result.Path,
				}
				switch {
				case err != nil:
					item.Status, item.Error = "failed", err.Error()
				case opts.DryRun:
					item.Status = "planned"
				case result.Skipped:
					item.Status = "skipped"
				default:
					item.Status = "imported"
				}
				output <- importOutcome{
					candidate: candidate, item: item, version: result.Version, err: err,
				}
			}
		}()
	}
	go func() {
		for _, candidate := range candidates {
			input <- candidate
		}
		close(input)
		workers.Wait()
		close(output)
	}()
	var outcomes []importOutcome
	for outcome := range output {
		outcomes = append(outcomes, outcome)
	}
	sort.Slice(outcomes, func(i, j int) bool {
		return outcomes[i].candidate.model.Ref() < outcomes[j].candidate.model.Ref()
	})
	return outcomes
}

func activate(repo Repository, key repoKey, candidates []candidate, versions map[identity]string, dryRun bool) (Item, error) {
	item := Item{Operation: "activate", Source: key.provider + ":" + key.repo}
	preferred := make([]candidate, 0, 1)
	for _, candidate := range candidates {
		if candidate.model.Preferred {
			preferred = append(preferred, candidate)
		}
	}
	var target candidate
	switch {
	case len(preferred) == 1:
		target = preferred[0]
	case len(preferred) > 1:
		err := fmt.Errorf("multiple provider-current revisions found for %s", item.Source)
		item.Status, item.Error = "failed", err.Error()
		return item, err
	case len(candidates) == 1:
		target = candidates[0]
	default:
		err := fmt.Errorf("cannot determine provider-current revision for %s", item.Source)
		item.Status, item.Error = "failed", err.Error()
		return item, err
	}
	item.Source = target.model.Ref()
	item.Name = target.name
	item.Version = versions[identity{repoKey: key, revision: target.model.Revision}]
	if item.Version == "" {
		err := fmt.Errorf("provider-current revision was not published for %s", target.model.Ref())
		item.Status, item.Error = "failed", err.Error()
		return item, err
	}
	if dryRun {
		item.Status = "planned"
		return item, nil
	}
	if err := repo.Activate(target.name+"@"+item.Version, false); err != nil {
		item.Status, item.Error = "failed", err.Error()
		return item, err
	}
	item.Status = "activated"
	return item, nil
}

func versionPath(existing []store.Version, candidate candidate) string {
	for _, version := range existing {
		if version.Name == candidate.name &&
			version.Manifest.Source.Provider == candidate.model.Provider &&
			version.Manifest.Source.Repo == candidate.model.Repo &&
			version.Manifest.Source.Revision == candidate.model.Revision {
			return version.Path
		}
	}
	return ""
}

func summarize(items []Item) Summary {
	var summary Summary
	for _, item := range items {
		switch item.Status {
		case "planned":
			summary.Planned++
		case "imported":
			summary.Imported++
		case "skipped":
			summary.Skipped++
		case "activated":
			summary.Activated++
		case "conflict":
			summary.Conflict++
		case "failed":
			summary.Failed++
		}
	}
	return summary
}

func operationOrder(operation string) int {
	switch operation {
	case "scan":
		return 0
	case "import":
		return 1
	case "activate":
		return 2
	default:
		return 3
	}
}

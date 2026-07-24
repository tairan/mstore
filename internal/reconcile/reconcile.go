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
	Provider   string
	Models     []string
	Configured bool
	Selections []Selection
	Activate   bool
	Hash       bool
	Jobs       int
	DryRun     bool
}

// Selection describes one exact cache revision selected by an editable model
// config. Name is the destination model key.
type Selection struct {
	Source source.Ref
	Name   string
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

type namedIdentity struct {
	identity
	name string
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
	if len(opts.Models) > 0 && opts.Configured {
		return report, SelectionError{Message: "model refs cannot be used with configured selections"}
	}
	existing, err := repo.List("")
	if err != nil {
		return report, err
	}

	namesByRepo := map[repoKey][]string{}
	seenName := map[repoKey]map[string]bool{}
	reposByName := map[string]repoKey{}
	versionsByIdentity := map[namedIdentity]string{}
	for _, version := range existing {
		key := repoKey{provider: version.Manifest.Source.Provider, repo: version.Manifest.Source.Repo}
		if prior, ok := reposByName[version.Name]; ok && prior != key {
			return report, fmt.Errorf("model name %q contains multiple source identities", version.Name)
		}
		if seenName[key] == nil {
			seenName[key] = map[string]bool{}
		}
		if !seenName[key][version.Name] {
			namesByRepo[key] = append(namesByRepo[key], version.Name)
			seenName[key][version.Name] = true
		}
		reposByName[version.Name] = key
		versionsByIdentity[namedIdentity{
			identity: identity{repoKey: key, revision: version.Manifest.Source.Revision},
			name:     version.Name,
		}] = version.Version
	}
	for key := range namesByRepo {
		sort.Strings(namesByRepo[key])
	}

	requested := make(map[string]bool, len(opts.Models))
	for _, name := range opts.Models {
		requested[name] = true
		key, ok := reposByName[name]
		if !ok {
			return report, SelectionError{Message: fmt.Sprintf("model %q is not registered", name)}
		}
		if opts.Provider != "all" && key.provider != opts.Provider {
			return report, SelectionError{Message: fmt.Sprintf("model %q uses provider %s, excluded by --provider %s", name, key.provider, opts.Provider)}
		}
	}
	selected := make(map[identity]Selection, len(opts.Selections))
	for _, selection := range opts.Selections {
		if selection.Source.Provider != "hf" && selection.Source.Provider != "ms" ||
			selection.Source.Repo == "" || selection.Source.Revision == "" {
			return report, SelectionError{Message: "configured selection requires provider, repository, and revision"}
		}
		if opts.Provider != "all" && selection.Source.Provider != opts.Provider {
			return report, SelectionError{Message: fmt.Sprintf("configured source %s:%s is excluded by --provider %s", selection.Source.Provider, selection.Source.Repo, opts.Provider)}
		}
		key := repoKey{provider: selection.Source.Provider, repo: selection.Source.Repo}
		id := identity{repoKey: key, revision: selection.Source.Revision}
		if _, exists := selected[id]; exists {
			return report, SelectionError{Message: fmt.Sprintf("configured source %s:%s@%s is duplicated", key.provider, key.repo, id.revision)}
		}
		if selection.Name == "" {
			name, nameErr := naming.Normalize(selection.Source.Repo)
			if nameErr != nil {
				return report, SelectionError{Message: nameErr.Error()}
			}
			selection.Name = name
		}
		selected[id] = selection
	}

	if opts.Configured && len(selected) == 0 {
		return report, nil
	}
	scanProvider := opts.Provider
	if opts.Configured && scanProvider == "all" {
		for id := range selected {
			if scanProvider == "all" {
				scanProvider = id.provider
				continue
			}
			if scanProvider != id.provider {
				scanProvider = "all"
				break
			}
		}
	}
	models, scanErrs := scan(scanProvider)
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
		id := identity{repoKey: key, revision: model.Revision}
		if seenIdentity[id] {
			continue
		}
		seenIdentity[id] = true
		names := namesByRepo[key]
		selection, configured := selected[id]
		if opts.Configured && !configured {
			continue
		}
		if configured {
			names = []string{selection.Name}
		} else if len(requested) > 0 {
			names = selectedNames(names, requested)
			if len(names) == 0 {
				continue
			}
		}
		if len(names) == 0 {
			name, normalizeErr := naming.Normalize(model.Repo)
			if normalizeErr != nil {
				report.Results = append(report.Results, Item{
					Operation: "import", Status: "failed", Source: model.Ref(), Error: normalizeErr.Error(),
				})
				if firstFailure == nil {
					firstFailure = normalizeErr
				}
				continue
			}
			names = []string{name}
		}
		for _, name := range names {
			candidates = append(candidates, candidate{
				model: model, key: key, name: name,
				version: versionsByIdentity[namedIdentity{identity: id, name: name}],
			})
		}
	}
	if opts.Configured {
		matched := make(map[identity]bool, len(candidates))
		for _, candidate := range candidates {
			matched[identity{repoKey: candidate.key, revision: candidate.model.Revision}] = true
		}
		for id := range selected {
			if matched[id] {
				continue
			}
			report.Results = append(report.Results, Item{
				Operation: "import", Status: "failed",
				Source: id.provider + ":" + id.repo + "@" + id.revision,
				Error:  "selected source is not ready in provider cache",
			})
			if firstFailure == nil {
				firstFailure = fmt.Errorf("selected source is not ready in provider cache")
			}
		}
	}

	conflicted := preflightConflicts(candidates, reposByName)
	modelCandidates := map[string][]candidate{}
	var imports []candidate
	for _, candidate := range candidates {
		modelCandidates[candidate.name] = append(modelCandidates[candidate.name], candidate)
		if message := conflicted[candidateKey(candidate)]; message != "" {
			report.Results = append(report.Results, Item{
				Operation: "import", Status: "conflict", Source: candidate.model.Ref(),
				Name: candidate.name, Error: message,
			})
			continue
		}
		if candidate.version != "" && !opts.Hash {
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
		versionsByIdentity[namedIdentity{
			identity: identity{repoKey: outcome.candidate.key, revision: outcome.candidate.model.Revision},
			name:     outcome.candidate.name,
		}] = outcome.version
	}

	if opts.Activate {
		names := make([]string, 0, len(modelCandidates))
		for name, candidates := range modelCandidates {
			if hasNoConflicts(candidates, conflicted) {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			item, activateErr := activate(repo, name, modelCandidates[name], versionsByIdentity, opts.DryRun)
			report.Results = append(report.Results, item)
			if activateErr != nil && firstFailure == nil {
				firstFailure = activateErr
			}
		}
	}

	sort.SliceStable(report.Results, func(i, j int) bool {
		if report.Results[i].Source == report.Results[j].Source {
			if report.Results[i].Name != report.Results[j].Name {
				return report.Results[i].Name < report.Results[j].Name
			}
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

func selectedNames(names []string, requested map[string]bool) []string {
	selected := make([]string, 0, len(names))
	for _, name := range names {
		if requested[name] {
			selected = append(selected, name)
		}
	}
	return selected
}

func candidateKey(candidate candidate) namedIdentity {
	return namedIdentity{
		identity: identity{repoKey: candidate.key, revision: candidate.model.Revision},
		name:     candidate.name,
	}
}

func hasNoConflicts(candidates []candidate, conflicted map[namedIdentity]string) bool {
	for _, candidate := range candidates {
		if conflicted[candidateKey(candidate)] != "" {
			return false
		}
	}
	return true
}

func preflightConflicts(candidates []candidate, existing map[string]repoKey) map[namedIdentity]string {
	conflicted := map[namedIdentity]string{}
	keysByName := map[string]map[repoKey]bool{}
	for _, candidate := range candidates {
		if owner, ok := existing[candidate.name]; ok {
			if owner != candidate.key {
				conflicted[candidateKey(candidate)] = fmt.Sprintf(
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
		for _, candidate := range candidates {
			if candidate.name == name && keys[candidate.key] {
				conflicted[candidateKey(candidate)] = fmt.Sprintf(
					"multiple new repositories normalize to %q; import each explicitly with --name", name,
				)
			}
		}
	}
	return conflicted
}

func importAll(repo Repository, candidates []candidate, opts Options) []importOutcome {
	if len(candidates) == 0 {
		return nil
	}
	groupsByName := map[string][]candidate{}
	for _, candidate := range candidates {
		groupsByName[candidate.name] = append(groupsByName[candidate.name], candidate)
	}
	names := make([]string, 0, len(groupsByName))
	for name := range groupsByName {
		names = append(names, name)
	}
	sort.Strings(names)
	jobs := min(opts.Jobs, len(names))
	input := make(chan []candidate)
	output := make(chan importOutcome, len(candidates))
	var workers sync.WaitGroup
	for range jobs {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for group := range input {
				for _, candidate := range group {
					output <- importOne(repo, candidate, opts)
				}
			}
		}()
	}
	go func() {
		for _, name := range names {
			input <- groupsByName[name]
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
		if outcomes[i].candidate.model.Ref() == outcomes[j].candidate.model.Ref() {
			return outcomes[i].candidate.name < outcomes[j].candidate.name
		}
		return outcomes[i].candidate.model.Ref() < outcomes[j].candidate.model.Ref()
	})
	return outcomes
}

func importOne(repo Repository, candidate candidate, opts Options) importOutcome {
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
	return importOutcome{candidate: candidate, item: item, version: result.Version, err: err}
}

func activate(repo Repository, name string, candidates []candidate, versions map[namedIdentity]string, dryRun bool) (Item, error) {
	key := candidates[0].key
	item := Item{Operation: "activate", Source: key.provider + ":" + key.repo, Name: name}
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
	item.Version = versions[namedIdentity{
		identity: identity{repoKey: key, revision: target.model.Revision}, name: name,
	}]
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

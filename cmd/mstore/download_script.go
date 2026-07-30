package main

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/chieworks/mstore/internal/fsutil"
	"github.com/chieworks/mstore/internal/manifest"
	"github.com/chieworks/mstore/internal/modelconfig"
	"github.com/chieworks/mstore/internal/source"
	"github.com/chieworks/mstore/internal/store"
)

const hfMirrorEndpoint = "https://hf-mirror.com"

type downloadScriptOptions struct {
	UseUV    bool
	HFMirror bool
}

type downloadScriptModel struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Current  bool   `json:"current"`
	Provider string `json:"provider"`
	Repo     string `json:"repo"`
	Revision string `json:"revision"`
	Command  string `json:"command"`
}

type downloadScriptPlan struct {
	Models []downloadScriptModel `json:"models"`
	Script string                `json:"script"`
}

type downloadSourceKey struct {
	Provider string
	Repo     string
	Revision string
}

type downloadScriptCommand struct {
	Commands []string
	Models   []downloadScriptImport
	CacheKey string
}

type downloadScriptImport struct {
	Name     string
	Version  string
	Activate bool
	Hash     bool
	Source   downloadSourceKey
}

func (a *app) downloadScript(args []string) error {
	f := newFlags("generate")
	all := f.Bool("all", false, "")
	currentOnly := f.Bool("current-only", false, "")
	configPath := f.String("config", "", "")
	useUV := f.Bool("uv", false, "")
	hfMirror := f.Bool("hf-mirror", false, "")
	if err := f.Parse(args); err != nil {
		return usageError("%v", err)
	}
	refs := f.Args()
	if *configPath != "" {
		if *all || len(refs) > 0 {
			return usageError("generate accepts --config or model refs/--all, not both")
		}
		if *currentOnly {
			return usageError("--current-only cannot be used with --config")
		}
		file, err := modelconfig.Read(*configPath)
		if err != nil {
			return fmt.Errorf("read config %s: %w", *configPath, err)
		}
		selections, err := modelconfig.Selections(file)
		if err != nil {
			return err
		}
		plan, err := makeConfigDownloadScript(selections, file.Defaults.Hash, downloadScriptOptions{UseUV: *useUV, HFMirror: *hfMirror})
		if err != nil {
			return err
		}
		if a.global.json {
			return writeJSON(a.out, plan)
		}
		_, err = fmt.Fprint(a.out, plan.Script)
		return err
	}
	if *all && len(refs) > 0 {
		return usageError("generate accepts model refs or --all, not both")
	}
	if !*all && len(refs) == 0 {
		return usageError("generate requires model refs or --all")
	}
	if *currentOnly && !*all {
		return usageError("--current-only requires --all")
	}

	versions, err := a.downloadScriptVersions(refs, *all, *currentOnly)
	if err != nil {
		return err
	}
	plan, err := makeDownloadScript(versions, downloadScriptOptions{UseUV: *useUV, HFMirror: *hfMirror})
	if err != nil {
		return err
	}
	if a.global.json {
		return writeJSON(a.out, plan)
	}
	_, err = fmt.Fprint(a.out, plan.Script)
	return err
}

func (a *app) downloadScriptVersions(refs []string, all, currentOnly bool) ([]store.Version, error) {
	if !all {
		selected := make(map[string]store.Version, len(refs))
		for _, ref := range refs {
			v, err := a.store.Resolve(ref)
			if err != nil {
				return nil, err
			}
			selected[v.Name+"@"+v.Version] = v
		}
		return sortedVersions(selected), nil
	}

	versions, err := a.store.List("")
	if err != nil {
		return nil, err
	}
	selected := make(map[string]store.Version, len(versions))
	for _, v := range versions {
		if currentOnly && !v.Current {
			continue
		}
		selected[v.Name+"@"+v.Version] = v
	}
	return sortedVersions(selected), nil
}

func sortedVersions(selected map[string]store.Version) []store.Version {
	versions := make([]store.Version, 0, len(selected))
	for _, v := range selected {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool {
		if versions[i].Name == versions[j].Name {
			return versions[i].Version < versions[j].Version
		}
		return versions[i].Name < versions[j].Name
	})
	return versions
}

func makeDownloadScript(versions []store.Version, opts downloadScriptOptions) (downloadScriptPlan, error) {
	plan := downloadScriptPlan{}
	type sourceGroup struct {
		files     map[string]bool
		models    []store.Version
		inventory []manifest.File
		selective bool
	}
	groups := make(map[downloadSourceKey]*sourceGroup, len(versions))
	var orderedKeys []downloadSourceKey
	fullSnapshots := make(map[downloadSourceKey]bool)
	for _, v := range versions {
		src := v.Manifest.Source
		if _, err := source.ParseRef(src.Provider + ":" + src.Repo + "@" + src.Revision); err != nil {
			return plan, fmt.Errorf("%s@%s: invalid source in manifest: %w", v.Name, v.Version, err)
		}
		key := downloadSourceKey{Provider: src.Provider, Repo: src.Repo, Revision: src.Revision}
		group := groups[key]
		if group == nil {
			group = &sourceGroup{files: make(map[string]bool), selective: true}
			groups[key] = group
			orderedKeys = append(orderedKeys, key)
		}
		files, err := storedFiles(v)
		if err != nil {
			return plan, fmt.Errorf("%s@%s: %w", v.Name, v.Version, err)
		}
		if len(group.models) > 0 {
			existingFiles, _, scanErr := fsutil.Scan(group.models[0].Path, true)
			if scanErr != nil {
				return plan, fmt.Errorf("%s@%s: scan alias inventory: %w", group.models[0].Name, group.models[0].Version, scanErr)
			}
			currentFiles, _, scanErr := fsutil.Scan(v.Path, true)
			if scanErr != nil {
				return plan, fmt.Errorf("%s@%s: scan alias inventory: %w", v.Name, v.Version, scanErr)
			}
			if !fsutil.SameTree(existingFiles, currentFiles, true) {
				return plan, fmt.Errorf("%s@%s: selected aliases for %s have different stored content; generate them separately", v.Name, v.Version, key.Provider+":"+key.Repo+"@"+key.Revision)
			}
		}
		if len(v.Manifest.Entries) == 0 {
			group.selective = false
			group.inventory = nil
			group.files = make(map[string]bool)
		} else if group.selective && len(group.inventory) == 0 {
			group.inventory = append([]manifest.File(nil), files...)
		} else if group.selective && !fsutil.SameTree(group.inventory, files, hasHashes(group.inventory) && hasHashes(files)) {
			return plan, fmt.Errorf("%s@%s: selected aliases for %s have different stored file inventories; generate them separately", v.Name, v.Version, key.Provider+":"+key.Repo+"@"+key.Revision)
		}
		group.models = append(group.models, v)
		if group.selective {
			for _, file := range files {
				group.files[file.Path] = true
			}
		}
	}
	commands := make(map[downloadSourceKey]downloadScriptCommand, len(groups))
	var warnings []string
	seenWarnings := make(map[string]bool)
	for _, key := range orderedKeys {
		group := groups[key]
		files := make([]string, 0, len(group.files))
		for file := range group.files {
			files = append(files, file)
		}
		sort.Strings(files)
		command, err := downloadCommands(key.Provider, key.Repo, key.Revision, files, opts)
		if err != nil {
			return plan, fmt.Errorf("%s@%s: %w", key.Repo, key.Revision, err)
		}
		commands[key] = downloadScriptCommand{
			Commands: command,
			Models:   storedImports(group.models),
			CacheKey: downloadCacheKey(key, files, !group.selective),
		}
		if !group.selective {
			fullSnapshots[key] = true
		}
		if fullSnapshots[key] {
			warning := "manifest for " + key.Provider + ":" + key.Repo + "@" + key.Revision + " has no complete file inventory; the generated script downloads the full snapshot."
			if !seenWarnings[warning] {
				seenWarnings[warning] = true
				warnings = append(warnings, warning)
			}
		}
		if key.Provider == "ms" && !isImmutableModelScopeRevision(key.Revision) {
			warning := "ModelScope revision " + key.Repo + "@" + key.Revision + " is not an immutable commit ID; it may move before the script runs."
			if !seenWarnings[warning] {
				seenWarnings[warning] = true
				warnings = append(warnings, warning)
			}
		}
	}
	for _, v := range versions {
		src := v.Manifest.Source
		key := downloadSourceKey{Provider: src.Provider, Repo: src.Repo, Revision: src.Revision}
		command := commands[key]
		plan.Models = append(plan.Models, downloadScriptModel{
			Name: v.Name, Version: v.Version, Current: v.Current,
			Provider: src.Provider, Repo: src.Repo, Revision: src.Revision,
			Command: strings.Join(command.Commands, "\n"),
		})
	}
	sort.Strings(warnings)

	plan.Script = renderDownloadScript(orderedKeys, commands, warnings, opts)
	return plan, nil
}

func makeConfigDownloadScript(selections []modelconfig.Selection, hash bool, opts downloadScriptOptions) (downloadScriptPlan, error) {
	plan := downloadScriptPlan{}
	commands := make(map[downloadSourceKey]downloadScriptCommand, len(selections))
	orderedKeys := make([]downloadSourceKey, 0, len(selections))
	var warnings []string
	for _, selection := range selections {
		key := downloadSourceKey{Provider: selection.Source.Provider, Repo: selection.Source.Repo, Revision: selection.Source.Revision}
		command, err := downloadCommands(key.Provider, key.Repo, key.Revision, nil, opts)
		if err != nil {
			return plan, fmt.Errorf("%s@%s: %w", key.Repo, key.Revision, err)
		}
		commands[key] = downloadScriptCommand{
			Commands: command,
			Models:   []downloadScriptImport{{Name: selection.Name, Hash: hash, Source: key}},
			CacheKey: downloadCacheKey(key, nil, true),
		}
		orderedKeys = append(orderedKeys, key)
		plan.Models = append(plan.Models, downloadScriptModel{
			Name: selection.Name, Provider: key.Provider, Repo: key.Repo, Revision: key.Revision,
			Command: strings.Join(command, "\n"),
		})
		if key.Provider == "ms" && !isImmutableModelScopeRevision(key.Revision) {
			warnings = append(warnings, "ModelScope revision "+key.Repo+"@"+key.Revision+" is not an immutable commit ID; it may move before the script runs.")
		}
	}
	plan.Script = renderDownloadScript(orderedKeys, commands, warnings, opts)
	return plan, nil
}

func storedImports(versions []store.Version) []downloadScriptImport {
	imports := make([]downloadScriptImport, 0, len(versions))
	for _, v := range versions {
		src := v.Manifest.Source
		imports = append(imports, downloadScriptImport{
			Name: v.Name, Version: v.Version, Activate: v.Current, Hash: hasHashes(v.Manifest.Entries),
			Source: downloadSourceKey{Provider: src.Provider, Repo: src.Repo, Revision: src.Revision},
		})
	}
	return imports
}

func renderDownloadScript(orderedKeys []downloadSourceKey, commands map[downloadSourceKey]downloadScriptCommand, warnings []string, opts downloadScriptOptions) string {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("# Generated by mstore generate. Recorded source revisions are used.\n")
	for _, warning := range warnings {
		b.WriteString("# WARNING: ")
		b.WriteString(warning)
		b.WriteString("\n")
	}
	if opts.UseUV {
		b.WriteString("# This script uses uvx; install uv before running it.\n")
	}
	if opts.HFMirror {
		b.WriteString("# Hugging Face downloads use https://hf-mirror.com.\n")
	}
	b.WriteString("# Authenticate with Hugging Face or ModelScope first when a model is private or gated.\n")
	b.WriteString("set -euo pipefail\n")
	if len(orderedKeys) > 0 {
		b.WriteString("# Set MSTORE_STORE to the destination store for imports.\n")
		b.WriteString("MSTORE_STORE=\"${MSTORE_STORE:-${MSTORE_HOME:-$HOME/models}}\"\n")
		b.WriteString("# Reuse isolated, mstore-owned provider caches for exact source revisions.\n")
		b.WriteString("MSTORE_DOWNLOAD_CACHE=\"${MSTORE_DOWNLOAD_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/mstore/downloads}\"\n")
		b.WriteString("case \"$MSTORE_DOWNLOAD_CACHE\" in\n")
		b.WriteString("  '~') MSTORE_DOWNLOAD_CACHE=\"$HOME\" ;;\n")
		b.WriteString("  '~/'*) MSTORE_DOWNLOAD_CACHE=\"$HOME/${MSTORE_DOWNLOAD_CACHE:2}\" ;;\n")
		b.WriteString("esac\n")
		b.WriteString("if [[ -e \"$MSTORE_DOWNLOAD_CACHE\" || -L \"$MSTORE_DOWNLOAD_CACHE\" ]]; then\n")
		b.WriteString("  if [[ -L \"$MSTORE_DOWNLOAD_CACHE\" || ! -d \"$MSTORE_DOWNLOAD_CACHE\" ]]; then\n")
		b.WriteString("    echo \"mstore: download cache must be a directory, not a symlink\" >&2\n")
		b.WriteString("    exit 1\n")
		b.WriteString("  fi\n")
		b.WriteString("else\n")
		b.WriteString("  mkdir -p \"$MSTORE_DOWNLOAD_CACHE\"\n")
		b.WriteString("fi\n")
		b.WriteString("if [[ -e \"$MSTORE_DOWNLOAD_CACHE/.mstore-download-cache\" || -L \"$MSTORE_DOWNLOAD_CACHE/.mstore-download-cache\" ]]; then\n")
		b.WriteString("  if [[ -L \"$MSTORE_DOWNLOAD_CACHE/.mstore-download-cache\" || ! -f \"$MSTORE_DOWNLOAD_CACHE/.mstore-download-cache\" ]]; then\n")
		b.WriteString("    echo \"mstore: download cache marker is not a regular file\" >&2\n")
		b.WriteString("    exit 1\n")
		b.WriteString("  fi\n")
		b.WriteString("else\n")
		b.WriteString("  shopt -s nullglob dotglob\n")
		b.WriteString("  MSTORE_CACHE_ENTRIES=(\"$MSTORE_DOWNLOAD_CACHE\"/*)\n")
		b.WriteString("  shopt -u nullglob dotglob\n")
		b.WriteString("  if (( ${#MSTORE_CACHE_ENTRIES[@]} != 0 )); then\n")
		b.WriteString("    echo \"mstore: refusing to mark a populated download cache\" >&2\n")
		b.WriteString("    exit 1\n")
		b.WriteString("  fi\n")
		b.WriteString("  : > \"$MSTORE_DOWNLOAD_CACHE/.mstore-download-cache\"\n")
		b.WriteString("fi\n")
	}
	for _, key := range orderedKeys {
		command := commands[key]
		b.WriteString("\nMSTORE_SOURCE_CACHE=\"$MSTORE_DOWNLOAD_CACHE/")
		b.WriteString(command.CacheKey)
		b.WriteString("\"\n")
		b.WriteString("export HF_HUB_CACHE=\"$MSTORE_SOURCE_CACHE/huggingface\"\n")
		b.WriteString("export MODELSCOPE_CACHE=\"$MSTORE_SOURCE_CACHE/modelscope\"\n")
		for _, text := range command.Commands {
			b.WriteString("\n")
			b.WriteString(text)
			b.WriteString("\n")
		}
		for _, model := range command.Models {
			b.WriteString("\nmstore --store \"$MSTORE_STORE\" import --name ")
			b.WriteString(shellQuote(model.Name))
			if model.Version != "" {
				b.WriteString(" --version ")
				b.WriteString(shellQuote(model.Version))
			}
			if model.Hash {
				b.WriteString(" --hash")
			}
			if model.Activate {
				b.WriteString(" --activate")
			}
			b.WriteByte(' ')
			b.WriteString(shellQuote(model.Source.Provider + ":" + model.Source.Repo + "@" + model.Source.Revision))
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func downloadCacheKey(key downloadSourceKey, files []string, fullSnapshot bool) string {
	identity := key.Provider + "\x00" + key.Repo + "\x00" + key.Revision + "\x00"
	if fullSnapshot {
		identity += "full"
	} else {
		identity += "files\x00" + strings.Join(files, "\x00")
	}
	sum := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("source-%x", sum[:16])
}

func storedFiles(v store.Version) ([]manifest.File, error) {
	fullHash := false
	for _, file := range v.Manifest.Entries {
		fullHash = fullHash || file.SHA256 != ""
	}
	files, bytes, err := fsutil.Scan(v.Path, fullHash)
	if err != nil {
		return nil, fmt.Errorf("scan stored files: %w", err)
	}
	if len(files) != v.Manifest.Files {
		return nil, fmt.Errorf("stored file inventory changed: manifest has %d files, found %d", v.Manifest.Files, len(files))
	}
	if bytes != v.Manifest.Bytes {
		return nil, fmt.Errorf("stored byte count changed: manifest has %d bytes, found %d", v.Manifest.Bytes, bytes)
	}
	if len(v.Manifest.Entries) > 0 {
		expected := append([]manifest.File(nil), v.Manifest.Entries...)
		sort.Slice(expected, func(i, j int) bool { return expected[i].Path < expected[j].Path })
		if !fsutil.SameTree(expected, files, fullHash) {
			return nil, fmt.Errorf("stored file inventory differs from manifest")
		}
	}
	return files, nil
}

func hasHashes(files []manifest.File) bool {
	if len(files) == 0 {
		return false
	}
	for _, file := range files {
		if file.SHA256 == "" {
			return false
		}
	}
	return true
}

func isImmutableModelScopeRevision(revision string) bool {
	if len(revision) != 40 && len(revision) != 64 {
		return false
	}
	for _, r := range revision {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

const maxDownloadCommandLength = 64 * 1024

func downloadCommands(provider, repo, revision string, files []string, opts downloadScriptOptions) ([]string, error) {
	if len(files) == 0 {
		command, err := downloadCommand(provider, repo, revision, files, opts)
		if err != nil {
			return nil, err
		}
		return []string{command}, nil
	}
	baseLength := len(downloadCommandPrefix(provider, repo, revision, opts)) + len(" --")
	var commands []string
	var chunk []string
	chunkLength := baseLength
	for _, file := range files {
		fileLength := len(" ") + len(shellQuote(file))
		if len(chunk) > 0 && chunkLength+fileLength > maxDownloadCommandLength {
			command, err := downloadCommand(provider, repo, revision, chunk, opts)
			if err != nil {
				return nil, err
			}
			commands = append(commands, command)
			chunk = nil
			chunkLength = baseLength
		}
		chunk = append(chunk, file)
		chunkLength += fileLength
	}
	if len(chunk) > 0 {
		command, err := downloadCommand(provider, repo, revision, chunk, opts)
		if err != nil {
			return nil, err
		}
		commands = append(commands, command)
	}
	return commands, nil
}

func downloadCommandPrefix(provider, repo, revision string, opts downloadScriptOptions) string {
	command, _ := downloadCommand(provider, repo, revision, nil, opts)
	return command
}

func downloadCommand(provider, repo, revision string, files []string, opts downloadScriptOptions) (string, error) {
	var command string
	switch provider {
	case "hf":
		command = "hf download " + shellQuote(repo) + " --revision " + shellQuote(revision)
		if len(files) > 0 {
			command += " --"
		}
		for _, file := range files {
			command += " " + shellQuote(file)
		}
		if opts.UseUV {
			command = "uvx --from huggingface_hub " + command
		}
		if opts.HFMirror {
			command = "HF_ENDPOINT=" + shellQuote(hfMirrorEndpoint) + " " + command
		}
	case "ms":
		command = "modelscope download --model " + shellQuote(repo) + " --revision " + shellQuote(revision)
		if len(files) > 0 {
			command += " --"
		}
		for _, file := range files {
			command += " " + shellQuote(file)
		}
		if opts.UseUV {
			command = "uvx " + command
		}
	default:
		return "", fmt.Errorf("unsupported provider %q", provider)
	}
	return command, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

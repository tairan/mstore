package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/chieworks/mstore/internal/fsutil"
	"github.com/chieworks/mstore/internal/manifest"
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
	Models   []store.Version
}

func (a *app) downloadScript(args []string) error {
	f := newFlags("generate")
	all := f.Bool("all", false, "")
	currentOnly := f.Bool("current-only", false, "")
	useUV := f.Bool("uv", false, "")
	hfMirror := f.Bool("hf-mirror", false, "")
	if err := f.Parse(args); err != nil {
		return usageError("%v", err)
	}
	refs := f.Args()
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
	}
	groups := make(map[downloadSourceKey]*sourceGroup, len(versions))
	var orderedKeys []downloadSourceKey
	for _, v := range versions {
		src := v.Manifest.Source
		if _, err := source.ParseRef(src.Provider + ":" + src.Repo + "@" + src.Revision); err != nil {
			return plan, fmt.Errorf("%s@%s: invalid source in manifest: %w", v.Name, v.Version, err)
		}
		key := downloadSourceKey{Provider: src.Provider, Repo: src.Repo, Revision: src.Revision}
		group := groups[key]
		if group == nil {
			group = &sourceGroup{files: make(map[string]bool)}
			groups[key] = group
			orderedKeys = append(orderedKeys, key)
		}
		files, err := storedFiles(v)
		if err != nil {
			return plan, fmt.Errorf("%s@%s: %w", v.Name, v.Version, err)
		}
		if len(group.inventory) == 0 {
			group.inventory = append([]manifest.File(nil), files...)
		} else if !fsutil.SameTree(group.inventory, files, hasHashes(group.inventory) && hasHashes(files)) {
			return plan, fmt.Errorf("%s@%s: selected aliases for %s have different stored file inventories; generate them separately", v.Name, v.Version, key.Provider+":"+key.Repo+"@"+key.Revision)
		}
		group.models = append(group.models, v)
		for _, file := range files {
			group.files[file.Path] = true
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
			Models:   group.models,
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
	if len(versions) > 0 {
		b.WriteString("# Set MSTORE_STORE to the destination store for imports.\n")
		b.WriteString("MSTORE_STORE=\"${MSTORE_STORE:-${MSTORE_HOME:-$HOME/models}}\"\n")
		b.WriteString("# Use isolated provider caches so pre-existing snapshots cannot contaminate imports.\n")
		b.WriteString("MSTORE_DOWNLOAD_CACHE=\"$(mktemp -d)\"\n")
		b.WriteString("trap 'rm -rf -- \"$MSTORE_DOWNLOAD_CACHE\"' EXIT\n")
	}
	for index, key := range orderedKeys {
		command := commands[key]
		b.WriteString("\nMSTORE_SOURCE_CACHE=\"$MSTORE_DOWNLOAD_CACHE/source-")
		b.WriteString(strconv.Itoa(index))
		b.WriteString("\"\n")
		b.WriteString("export HF_HUB_CACHE=\"$MSTORE_SOURCE_CACHE/huggingface\"\n")
		b.WriteString("export MODELSCOPE_CACHE=\"$MSTORE_SOURCE_CACHE/modelscope\"\n")
		for _, text := range command.Commands {
			b.WriteString("\n")
			b.WriteString(text)
			b.WriteString("\n")
		}
		for _, v := range command.Models {
			b.WriteString("\nmstore --store \"$MSTORE_STORE\" import --name ")
			b.WriteString(shellQuote(v.Name))
			b.WriteString(" --version ")
			b.WriteString(shellQuote(v.Version))
			if v.Current {
				b.WriteString(" --activate")
			}
			b.WriteByte(' ')
			b.WriteString(shellQuote(v.Manifest.Source.Provider + ":" + v.Manifest.Source.Repo + "@" + v.Manifest.Source.Revision))
			b.WriteByte('\n')
		}
	}
	plan.Script = b.String()
	return plan, nil
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

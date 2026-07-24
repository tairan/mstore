package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/chieworks/mstore/internal/fsutil"
	"github.com/chieworks/mstore/internal/modelconfig"
	"github.com/chieworks/mstore/internal/naming"
	"github.com/chieworks/mstore/internal/providers"
	"github.com/chieworks/mstore/internal/reconcile"
	"github.com/chieworks/mstore/internal/source"
	"github.com/chieworks/mstore/internal/store"
)

var version = "0.1.0"

type globalOptions struct {
	store   string
	json    bool
	quiet   bool
	verbose int
}

type app struct {
	global globalOptions
	store  *store.Store
	out    io.Writer
	err    io.Writer
}

func main() {
	code := run(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}

func run(args []string, out, errOut io.Writer) int {
	g, command, rest, err := parseGlobal(args)
	if err != nil {
		fmt.Fprintln(errOut, "mstore:", err)
		return 2
	}
	if command == "" || command == "help" {
		printHelp(out)
		return 0
	}
	if command == "version" {
		fmt.Fprintln(out, buildVersion())
		return 0
	}
	s, err := store.Open(g.store)
	if err != nil {
		fmt.Fprintln(errOut, "mstore:", err)
		return 1
	}
	a := app{global: g, store: s, out: out, err: errOut}
	if err := a.dispatch(command, rest); err != nil {
		fmt.Fprintln(errOut, "mstore:", err)
		return exitCode(err)
	}
	return 0
}

func parseGlobal(args []string) (globalOptions, string, []string, error) {
	var g globalOptions
	for len(args) > 0 {
		arg := args[0]
		switch {
		case arg == "--store":
			if len(args) < 2 {
				return g, "", nil, fmt.Errorf("--store requires a path")
			}
			g.store, args = args[1], args[2:]
		case strings.HasPrefix(arg, "--store="):
			g.store, args = strings.TrimPrefix(arg, "--store="), args[1:]
		case arg == "--json":
			g.json, args = true, args[1:]
		case arg == "-q" || arg == "--quiet":
			g.quiet, args = true, args[1:]
		case arg == "-v" || arg == "--verbose":
			g.verbose++
			args = args[1:]
		case arg == "-vv":
			g.verbose += 2
			args = args[1:]
		case arg == "--no-color":
			args = args[1:]
		case arg == "-h" || arg == "--help":
			return g, "help", nil, nil
		case arg == "-V" || arg == "--version":
			return g, "version", nil, nil
		case strings.HasPrefix(arg, "-"):
			return g, "", nil, fmt.Errorf("unknown global option %s", arg)
		default:
			return g, arg, args[1:], nil
		}
	}
	return g, "", nil, nil
}

func (a *app) dispatch(command string, args []string) error {
	switch command {
	case "scan":
		return a.scan(args)
	case "import":
		return a.importModels(args)
	case "sync":
		return a.sync(args)
	case "config":
		return a.config(args)
	case "generate", "gen":
		return a.downloadScript(args)
	case "list", "ls":
		return a.list(args)
	case "show":
		return a.show(args)
	case "path":
		return a.path(args)
	case "activate":
		return a.activate(args)
	case "rename":
		return a.rename(args)
	case "verify":
		return a.verify(args)
	case "copy", "cp":
		return a.copy(args)
	case "remove", "rm":
		return a.remove(args)
	case "gc":
		return a.gc(args)
	case "doctor":
		return a.doctor(args)
	case "completion":
		return a.completion(args)
	default:
		return usageError("unknown command %q", command)
	}
}

func newFlags(name string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	return f
}

func (a *app) scan(args []string) error {
	f := newFlags("scan")
	provider := f.String("provider", "all", "")
	readyOnly := f.Bool("ready-only", false, "")
	newOnly := f.Bool("new-only", false, "")
	long := f.Bool("long", false, "")
	if err := f.Parse(args); err != nil {
		return usageError("%v", err)
	}
	if err := validProvider(*provider); err != nil {
		return err
	}
	models, scanErrs := providers.Scan(*provider)
	versions, _ := a.store.List("")
	for i := range models {
		imported := false
		for _, v := range versions {
			if v.Manifest.Source.Provider == models[i].Provider &&
				v.Manifest.Source.Repo == models[i].Repo &&
				v.Manifest.Source.Revision == models[i].Revision {
				models[i].Status = "imported"
				imported = true
			}
		}
		if imported || models[i].Status != "ready" {
			continue
		}
		defaultName, nameErr := naming.Normalize(models[i].Repo)
		if nameErr != nil {
			models[i].Status, models[i].Error = "invalid", nameErr.Error()
			continue
		}
		for _, v := range versions {
			if v.Name == defaultName &&
				(v.Manifest.Source.Provider != models[i].Provider || v.Manifest.Source.Repo != models[i].Repo) {
				models[i].Status = "conflict"
				models[i].Error = "default model name is already used; import with --name"
				break
			}
		}
	}
	var filtered []source.Model
	for _, m := range models {
		if *readyOnly && m.Status != "ready" {
			continue
		}
		if *newOnly && m.Status != "ready" {
			continue
		}
		filtered = append(filtered, m)
	}
	if a.global.json {
		return writeJSON(a.out, map[string]any{"models": filtered, "errors": errorsToStrings(scanErrs)})
	}
	for _, m := range filtered {
		if *long {
			fmt.Fprintf(a.out, "%-10s %-8s %s\n", m.Status, m.Provider, m.Ref())
			if m.Error != "" {
				fmt.Fprintf(a.out, "  %s\n", m.Error)
			}
		} else {
			fmt.Fprintf(a.out, "%-10s %s\n", m.Status, m.Ref())
		}
	}
	for _, err := range scanErrs {
		fmt.Fprintln(a.err, "mstore scan:", err)
	}
	return nil
}

func (a *app) importModels(args []string) error {
	f := newFlags("import")
	name := f.String("name", "", "")
	version := f.String("version", "", "")
	activate := f.Bool("activate", false, "")
	hash := f.Bool("hash", false, "")
	jobs := f.Int("jobs", 1, "")
	dryRun := f.Bool("dry-run", false, "")
	if err := f.Parse(args); err != nil {
		return usageError("%v", err)
	}
	if *jobs < 1 {
		return usageError("--jobs must be at least 1")
	}
	refs := f.Args()
	if len(refs) == 0 {
		return usageError("provide SOURCE...")
	}
	if (*name != "" || *version != "") && len(refs) != 1 {
		return usageError("--name and --version are only valid with one source")
	}
	var models []source.Model
	for _, raw := range refs {
		r, err := source.ParseRef(raw)
		if err != nil {
			return usageError("%s: %v", raw, err)
		}
		m, err := providers.Resolve(r)
		if err != nil {
			return err
		}
		models = append(models, m)
	}
	var results []store.ImportResult
	for _, m := range models {
		res, err := a.store.Import(m, store.ImportOptions{Name: *name, Version: *version, Activate: *activate, Hash: *hash, DryRun: *dryRun})
		if err != nil {
			return err
		}
		results = append(results, res)
	}
	return a.printImportResults(results)
}

func (a *app) sync(args []string) error {
	f := newFlags("sync")
	provider := f.String("provider", "all", "")
	configPath := f.String("config", "", "")
	activate := f.Bool("activate", false, "")
	hash := f.Bool("hash", false, "")
	jobs := f.Int("jobs", 1, "")
	dryRun := f.Bool("dry-run", false, "")
	if err := f.Parse(args); err != nil {
		return usageError("%v", err)
	}
	if err := validProvider(*provider); err != nil {
		return err
	}
	if *configPath != "" && len(f.Args()) > 0 {
		return usageError("model refs cannot be used with --config")
	}
	options := reconcile.Options{
		Provider: *provider,
		Models:   f.Args(),
		Activate: *activate,
		Hash:     *hash,
		Jobs:     *jobs,
		DryRun:   *dryRun,
	}
	if *configPath != "" {
		file, err := modelconfig.Read(*configPath)
		if err != nil {
			return fmt.Errorf("read config %s: %w", *configPath, err)
		}
		selections, err := modelconfig.Selections(file)
		if err != nil {
			return err
		}
		options.Hash = options.Hash || file.Defaults.Hash
		options.Configured = true
		options.Selections = make([]reconcile.Selection, len(selections))
		for i, selection := range selections {
			options.Selections[i] = reconcile.Selection{Source: selection.Source, Name: selection.Name}
		}
	}
	report, syncErr := reconcile.Run(a.store, providers.Scan, reconcile.Options{
		Provider: options.Provider, Models: options.Models, Configured: options.Configured, Selections: options.Selections,
		Activate: options.Activate, Hash: options.Hash, Jobs: options.Jobs, DryRun: options.DryRun,
	})
	var selectionErr reconcile.SelectionError
	if errors.As(syncErr, &selectionErr) {
		return usageError("%v", selectionErr)
	}
	if err := a.printSyncReport(report); err != nil {
		return err
	}
	return syncErr
}

func (a *app) config(args []string) error {
	if len(args) == 0 {
		return usageError("config requires export or check")
	}
	switch args[0] {
	case "export":
		f := newFlags("config export")
		output := f.String("output", "models.toml", "")
		provider := f.String("provider", "all", "")
		overwrite := f.Bool("overwrite", false, "")
		if err := f.Parse(args[1:]); err != nil {
			return usageError("%v", err)
		}
		if *output == "" || len(f.Args()) != 0 {
			return usageError("config export accepts no positional arguments")
		}
		if err := validProvider(*provider); err != nil {
			return err
		}
		path := modelconfig.OutputPath(*output)
		models, scanErrs := providers.Scan(*provider)
		for _, scanErr := range scanErrs {
			fmt.Fprintln(a.err, "mstore config export:", scanErr)
		}
		if err := modelconfig.Export(path, models, *overwrite); err != nil {
			return err
		}
		if a.global.json {
			return writeJSON(a.out, map[string]any{"path": path, "models": len(models)})
		}
		if !a.global.quiet {
			fmt.Fprintln(a.out, "exported", path)
		}
		return nil
	case "check":
		if len(args) != 2 {
			return usageError("config check requires FILE")
		}
		file, err := modelconfig.Read(args[1])
		if err != nil {
			return err
		}
		selections, err := modelconfig.Selections(file)
		if err != nil {
			return err
		}
		if a.global.json {
			return writeJSON(a.out, map[string]any{"valid": true, "models": len(file.Models), "enabled": len(selections)})
		}
		if !a.global.quiet {
			fmt.Fprintf(a.out, "ok %d models, %d enabled\n", len(file.Models), len(selections))
		}
		return nil
	default:
		return usageError("unknown config command %q", args[0])
	}
}

func (a *app) printSyncReport(report reconcile.Report) error {
	if a.global.json {
		return writeJSON(a.out, report)
	}
	if a.global.quiet {
		return nil
	}
	for _, item := range report.Results {
		if item.Status == "skipped" && a.global.verbose == 0 {
			continue
		}
		target := item.Source
		if item.Name != "" {
			target = item.Name
			if item.Version != "" {
				target += "@" + item.Version
			}
		}
		fmt.Fprintf(a.out, "%-9s %-8s %s", item.Status, item.Operation, target)
		if item.Path != "" {
			fmt.Fprintf(a.out, " -> %s", item.Path)
		}
		if item.Error != "" {
			fmt.Fprintf(a.out, ": %s", item.Error)
		}
		fmt.Fprintln(a.out)
	}
	fmt.Fprintf(a.out,
		"summary: planned=%d imported=%d skipped=%d activated=%d conflict=%d failed=%d\n",
		report.Summary.Planned, report.Summary.Imported, report.Summary.Skipped,
		report.Summary.Activated, report.Summary.Conflict, report.Summary.Failed,
	)
	return nil
}

func (a *app) printImportResults(results []store.ImportResult) error {
	if a.global.json {
		return writeJSON(a.out, results)
	}
	if a.global.quiet {
		return nil
	}
	for _, r := range results {
		action := "imported"
		if r.Skipped {
			action = "skipped"
		}
		fmt.Fprintf(a.out, "%s %s@%s -> %s\n", action, r.Name, r.Version, r.Path)
	}
	return nil
}

func (a *app) list(args []string) error {
	f := newFlags("list")
	showVersions := f.Bool("versions", false, "")
	showSource := f.Bool("source", false, "")
	long := f.Bool("long", false, "")
	if err := f.Parse(args); err != nil {
		return usageError("%v", err)
	}
	if len(f.Args()) > 1 {
		return usageError("list accepts at most one model")
	}
	model := ""
	if len(f.Args()) == 1 {
		model = f.Args()[0]
	}
	versions, err := a.store.List(model)
	if err != nil {
		return err
	}
	if a.global.json {
		return writeJSON(a.out, versions)
	}
	seen := map[string]bool{}
	for _, v := range versions {
		if !*showVersions && model == "" {
			if seen[v.Name] {
				continue
			}
			seen[v.Name] = true
			fmt.Fprintln(a.out, v.Name)
			continue
		}
		line := v.Name + "@" + v.Version
		if v.Current {
			line += " *"
		}
		if *showSource || *long {
			line += "  " + v.Manifest.Source.Provider + ":" + v.Manifest.Source.Repo + "@" + v.Manifest.Source.Revision
		}
		if *long {
			line += fmt.Sprintf("  %d files  %d bytes", v.Manifest.Files, v.Manifest.Bytes)
		}
		fmt.Fprintln(a.out, line)
	}
	return nil
}

func (a *app) show(args []string) error {
	f := newFlags("show")
	files := f.Bool("files", false, "")
	hashes := f.Bool("hashes", false, "")
	if err := f.Parse(args); err != nil {
		return usageError("%v", err)
	}
	if len(f.Args()) != 1 {
		return usageError("show requires one model or model@version")
	}
	v, err := a.store.Resolve(f.Args()[0])
	if err != nil {
		return err
	}
	m := v.Manifest
	if *files || *hashes {
		entries, _, err := fsutil.Scan(v.Path, *hashes)
		if err != nil {
			return err
		}
		m.Entries = entries
	}
	if a.global.json {
		return writeJSON(a.out, m)
	}
	fmt.Fprintf(a.out, "name: %s\nversion: %s\nsource: %s:%s@%s\nfiles: %d\nbytes: %d\nimported: %s\n",
		m.Name, m.Version, m.Source.Provider, m.Source.Repo, m.Source.Revision, m.Files, m.Bytes, m.ImportedAt.Format(time.RFC3339))
	for _, entry := range m.Entries {
		if *hashes {
			fmt.Fprintf(a.out, "%s  %d  %s\n", entry.SHA256, entry.Size, entry.Path)
		} else {
			fmt.Fprintf(a.out, "%d  %s\n", entry.Size, entry.Path)
		}
	}
	return nil
}

func (a *app) path(args []string) error {
	f := newFlags("path")
	link := f.Bool("link", false, "")
	if err := f.Parse(args); err != nil {
		return usageError("%v", err)
	}
	if len(f.Args()) != 1 {
		return usageError("path requires one model or model@version")
	}
	v, err := a.store.Resolve(f.Args()[0])
	if err != nil {
		return err
	}
	p := v.Path
	if *link {
		p = filepath.Join(a.store.Root, v.Name, "current")
	}
	if a.global.json {
		return writeJSON(a.out, map[string]string{"path": p})
	}
	fmt.Fprintln(a.out, p)
	return nil
}

func (a *app) activate(args []string) error {
	f := newFlags("activate")
	noVerify := f.Bool("no-verify", false, "")
	if err := f.Parse(args); err != nil {
		return usageError("%v", err)
	}
	if len(f.Args()) != 1 {
		return usageError("activate requires model@version")
	}
	if err := a.store.Activate(f.Args()[0], *noVerify); err != nil {
		return err
	}
	return a.success(map[string]any{"activated": f.Args()[0]}, "activated "+f.Args()[0])
}

func (a *app) rename(args []string) error {
	f := newFlags("rename")
	dryRun := f.Bool("dry-run", false, "")
	if err := f.Parse(args); err != nil {
		return usageError("%v", err)
	}
	if len(f.Args()) != 2 {
		return usageError("rename requires old-name and new-name")
	}
	if err := a.store.Rename(f.Args()[0], f.Args()[1], *dryRun); err != nil {
		return err
	}
	return a.success(map[string]any{"old": f.Args()[0], "new": f.Args()[1], "dry_run": *dryRun}, fmt.Sprintf("renamed %s -> %s", f.Args()[0], f.Args()[1]))
}

func (a *app) verify(args []string) error {
	f := newFlags("verify")
	all := f.Bool("all", false, "")
	full := f.Bool("full", false, "")
	record := f.Bool("record", false, "")
	jobs := f.Int("jobs", 1, "")
	if err := f.Parse(args); err != nil {
		return usageError("%v", err)
	}
	refs := f.Args()
	if (*all && len(refs) > 0) || (!*all && len(refs) == 0) || *jobs < 1 {
		return usageError("provide model refs or --all; --jobs must be at least 1")
	}
	if *all {
		versions, err := a.store.List("")
		if err != nil {
			return err
		}
		for _, v := range versions {
			refs = append(refs, v.Name+"@"+v.Version)
		}
	}
	type verified struct {
		Ref string `json:"ref"`
		OK  bool   `json:"ok"`
	}
	var out []verified
	for _, ref := range refs {
		if _, err := a.store.Verify(ref, *full, *record); err != nil {
			return fmt.Errorf("%s: %w", ref, err)
		}
		out = append(out, verified{ref, true})
	}
	if a.global.json {
		return writeJSON(a.out, out)
	}
	for _, v := range out {
		fmt.Fprintln(a.out, "ok", v.Ref)
	}
	return nil
}

func (a *app) copy(args []string) error {
	f := newFlags("copy")
	to := f.String("to", "", "")
	all := f.Bool("all", false, "")
	allVersions := f.Bool("all-versions", false, "")
	currentOnly := f.Bool("current-only", false, "")
	verify := f.String("verify", "quick", "")
	jobs := f.Int("jobs", 1, "")
	dryRun := f.Bool("dry-run", false, "")
	if err := f.Parse(args); err != nil {
		return usageError("%v", err)
	}
	if *to == "" {
		return usageError("copy requires --to")
	}
	if *jobs < 1 || (*verify != "none" && *verify != "quick" && *verify != "full") {
		return usageError("invalid --jobs or --verify")
	}
	refs := f.Args()
	if !*all && len(refs) == 0 {
		return usageError("copy requires model refs or --all")
	}
	dst, err := store.Open(*to)
	if err != nil {
		return err
	}
	results, err := a.store.CopyTo(dst, refs, *allVersions, *currentOnly, *verify == "full", *dryRun)
	if err != nil {
		return err
	}
	return a.printImportResults(results)
}

func (a *app) remove(args []string) error {
	f := newFlags("remove")
	inactive := f.Bool("inactive", false, "")
	allVersions := f.Bool("all-versions", false, "")
	force := f.Bool("force", false, "")
	yes := f.Bool("yes", false, "")
	dryRun := f.Bool("dry-run", false, "")
	if err := f.Parse(args); err != nil {
		return usageError("%v", err)
	}
	if len(f.Args()) != 1 {
		return usageError("remove requires one model ref")
	}
	if !*yes && !*dryRun {
		return usageError("remove requires --yes (or use --dry-run)")
	}
	removed, err := a.store.Remove(f.Args()[0], *inactive, *allVersions, *force, *dryRun)
	if err != nil {
		return err
	}
	if a.global.json {
		return writeJSON(a.out, map[string]any{"removed": removed, "dry_run": *dryRun})
	}
	for _, ref := range removed {
		fmt.Fprintln(a.out, "removed", ref)
	}
	return nil
}

func (a *app) gc(args []string) error {
	f := newFlags("gc")
	older := f.Duration("older-than", 24*time.Hour, "")
	dryRun := f.Bool("dry-run", false, "")
	if err := f.Parse(args); err != nil {
		return usageError("%v", err)
	}
	removed, err := a.store.GC(*older, *dryRun)
	if err != nil {
		return err
	}
	if a.global.json {
		return writeJSON(a.out, map[string]any{"removed": removed, "dry_run": *dryRun})
	}
	for _, p := range removed {
		fmt.Fprintln(a.out, p)
	}
	return nil
}

func (a *app) doctor(args []string) error {
	f := newFlags("doctor")
	provider := f.String("provider", "all", "")
	writeTest := f.Bool("write-test", false, "")
	if err := f.Parse(args); err != nil {
		return usageError("%v", err)
	}
	if err := validProvider(*provider); err != nil {
		return err
	}
	results := a.store.Doctor(*writeTest)
	_, scanErrs := providers.Scan(*provider)
	results = append(results, store.DoctorResult{Check: "provider-cache", OK: len(scanErrs) == 0, Detail: strings.Join(errorsToStrings(scanErrs), "; ")})
	if a.global.json {
		return writeJSON(a.out, results)
	}
	failed := false
	for _, r := range results {
		status := "ok"
		if !r.OK {
			status, failed = "FAIL", true
		}
		fmt.Fprintf(a.out, "%-4s %-16s %s\n", status, r.Check, r.Detail)
	}
	if failed {
		return fmt.Errorf("doctor found problems")
	}
	return nil
}

func (a *app) completion(args []string) error {
	if len(args) != 1 {
		return usageError("completion requires bash, zsh, fish, or powershell")
	}
	shell := args[0]
	var script string
	switch shell {
	case "bash":
		script = `complete -W "scan import sync generate gen list ls show path activate rename verify copy cp remove rm gc doctor completion help" mstore`
	case "zsh":
		script = `compdef '_arguments "1:command:(scan import sync generate gen list ls show path activate rename verify copy cp remove rm gc doctor completion help)"' mstore`
	case "fish":
		script = `complete -c mstore -f -a "scan import sync generate gen list ls show path activate rename verify copy cp remove rm gc doctor completion help"`
	case "powershell":
		script = `Register-ArgumentCompleter -CommandName mstore -ScriptBlock { param($w,$a,$p) "scan","import","sync","generate","gen","list","show","path","activate","rename","verify","copy","remove","gc","doctor","completion","help" | Where-Object { $_ -like "$w*" } }`
	default:
		return usageError("unsupported shell %q", shell)
	}
	fmt.Fprintln(a.out, script)
	return nil
}

func (a *app) success(value any, text string) error {
	if a.global.json {
		return writeJSON(a.out, value)
	}
	if !a.global.quiet {
		fmt.Fprintln(a.out, text)
	}
	return nil
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `mstore publishes already-downloaded model caches into an immutable local store.

Usage:
  mstore [global options] <command> [options]

Commands:
  scan                 Inspect Hugging Face and ModelScope caches.
  import               Publish one or more cached sources.
  sync                 Publish ready cached revisions.
  config export        Export an editable model selection file.
  config check         Validate a model selection file.
  generate, gen        Generate a Bash download script from stored manifests.
  list, ls             List stored models and versions.
  show                 Show a model manifest.
  path                 Print a model's physical path.
  activate             Atomically switch current.
  rename               Rename a stored model.
  verify               Verify stored files.
  copy, cp             Copy into another local mstore.
  remove, rm           Remove stored versions.
  gc                   Clean stale staging, parts, and locks.
  doctor               Diagnose store and cache health.
  completion           Generate shell completion.
  help                 Show this help.

Global options:
  --store PATH       Store root (default: ${MSTORE_HOME:-~/models}).
  --json             Emit stable JSON output.
  -q, --quiet        Suppress normal output.
  -v, -vv            Increase diagnostics.
  --no-color         Disable color.
  -V, --version      Show version.

Model config:
  mstore config export [--output FILE] [--provider hf|ms|all] [--overwrite]
      Write ./models.toml by default. Existing files are protected unless
      --overwrite is supplied.
  mstore config check FILE
      Validate a TOML model selection file.
  mstore sync --config FILE [--activate] [--hash] [--jobs N] [--dry-run]
      Publish only enabled, exact revisions in FILE.

Source references:
  hf:NAMESPACE/REPO[@REVISION]
  ms:NAMESPACE/REPO[@REVISION]

Selected command options:
  import:    --name NAME  --version VER  --activate  --hash  --jobs N  --dry-run
  sync:      --provider hf|ms|all  --config FILE  --activate  --hash  --jobs N  --dry-run
  generate:  --all  --current-only  --uv  --hf-mirror

Examples:
  mstore sync
  mstore config export
  mstore sync --config models.toml --dry-run
  mstore generate --all > download-models.sh
  mstore generate --uv --hf-mirror --all > download-models.sh
`)
}

type cliError struct{ msg string }

func (e cliError) Error() string { return e.msg }
func usageError(format string, args ...any) error {
	return cliError{fmt.Sprintf(format, args...)}
}

func exitCode(err error) int {
	var usage cliError
	if errors.As(err, &usage) {
		return 2
	}
	var scanErr providers.ScanError
	if errors.As(err, &scanErr) {
		return 3
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "lock timeout"):
		return 6
	case strings.Contains(s, "verification failed"), strings.Contains(s, "differs from source"):
		return 5
	case strings.Contains(s, "conflict"), strings.Contains(s, "already used"), strings.Contains(s, "collision"):
		return 4
	case strings.Contains(s, "cache"), strings.Contains(s, "source not found"), strings.Contains(s, "incomplete"):
		return 3
	case strings.Contains(s, "no space"):
		return 7
	default:
		return 1
	}
}

func validProvider(p string) error {
	if p != "all" && p != "hf" && p != "ms" {
		return usageError("--provider must be all, hf, or ms")
	}
	return nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func errorsToStrings(errs []error) []string {
	out := make([]string, len(errs))
	for i, err := range errs {
		out[i] = err.Error()
	}
	return out
}

func buildVersion() string {
	v := version
	if info, ok := debug.ReadBuildInfo(); ok && v == "dev" {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && len(setting.Value) >= 12 {
				v = setting.Value[:12]
			}
		}
	}
	return "mstore " + v
}

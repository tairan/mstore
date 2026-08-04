# mstore

**English** | [简体中文](README.zh-CN.md)

[![CI](https://github.com/tairan/mstore/actions/workflows/ci.yml/badge.svg)](https://github.com/tairan/mstore/actions/workflows/ci.yml)

`mstore` publishes models that are already present in the native Hugging Face
or ModelScope cache into a portable, immutable local model store. It does not
download models itself. It can generate provider download scripts and, with an
explicit `--yes`, prune identifiable abnormal entries from provider caches.

The resulting directories are ordinary files, so they can be mounted
read-only into containers, copied to mounted disks, backed up, or moved to
another machine without a database.

## Install and build

The repository pins its Go toolchain with [mise](https://mise.jdx.dev/):

```sh
mise install
mise run check
mise run build
```

The current-platform static binary is written to `bin/mstore`.
`mise run build-all` reproducibly creates `dist/mstore-linux-amd64`. ARM64
builds are not supported. Builds set `CGO_ENABLED=0`; mstore has no Python,
rsync, database, daemon, or CGO runtime dependency.

Available tasks are `fmt`, `lint`, `test`, `check`, `build`, and `build-all`.

## Continuous integration and releases

GitHub Actions runs `mise run check` and builds the static Linux amd64 binary
for pull requests, pushes to `main`, and manual workflow runs.

Push a strict [SemVer](https://semver.org/) tag to create a GitHub Release:

```sh
git tag v1.2.3
git push origin v1.2.3
```

Prerelease tags such as `v1.2.3-rc.1` create prereleases. Each release contains
`mstore-linux-amd64` and `mstore-linux-amd64.sha256`; the version is embedded
in the binary. `MSTORE_VERSION` can also set the embedded version during a
local build:

```sh
MSTORE_VERSION=v1.2.3 mise run build-all
```

## Provider caches

Hugging Face cache lookup follows this order:

1. `HF_HUB_CACHE`
2. `$HF_HOME/hub`
3. `~/.cache/huggingface/hub`

The standard `models--namespace--repo/{blobs,snapshots,refs}` layout is
supported. Snapshot symlinks are followed while copying; published models
contain regular files. Dangling links and incomplete/temporary files are
rejected.

ModelScope cache lookup is:

1. `$MODELSCOPE_CACHE/models`
2. `~/.cache/modelscope/hub/models`

Only the current ModelScope CLI cache layout, `models/<namespace>/<repo>/`, is
supported. Repository directory names encode `.` as `___`; `.mv` contains the
revision (either the revision alone or `Revision:<revision>,CreatedAt:<time>`).
A valid `.mv` and a non-empty valid file tree are required for a ready source.

## Store layout

The default store is `${MSTORE_HOME:-~/models}` and `--store PATH` overrides it:

```text
<store>/
├── <model-key>/
│   ├── <version>/
│   │   ├── ...model files...
│   │   └── .mstore.json
│   └── current -> <version>
├── .stage/
└── .locks/
```

A model key is the mechanically normalized repository basename: lowercase,
spaces and underscores become `-`, and repeated separators collapse. Keys are
ASCII and at most 64 bytes; they are never silently truncated. Use `--name`
to resolve collisions. Versions start with the first 12 bytes of the full
revision and expand when a prefix collides. The manifest retains the complete
identity: provider, repository, and revision.

Import uses a per-model lock, a deterministic staging directory, resumable
`.part` files, fsync, source-tree scans before and after copying, staging
verification, and a same-filesystem atomic rename. Existing matching
identities are skipped. Published version directories are never overwritten.

## Typical use

Scan the native caches and publish completed revisions:

```sh
mstore scan --provider all --long
mstore sync --dry-run
mstore sync
mstore import --activate hf:Qwen/Qwen3-TTS-12Hz-1.7B-CustomVoice@COMMIT
mstore sync --activate qwen3-tts-12hz-1-7b-customvoice
```

With no model arguments, `sync` scans every provider and imports every ready
revision, including repositories that have never been imported before. Use
`--provider hf|ms|all` to restrict discovery. Missing provider caches are
skipped, incomplete revisions are ignored, and one failed model does not stop
the others. Name conflicts fail without choosing an arbitrary owner.

`sync` does not change `current` unless `--activate` is supplied. With
activation enabled, Hugging Face prefers `refs/main` then `refs/master`, and
ModelScope prefers the `master` revision; a repository with exactly one ready revision uses
that revision as a fallback.

### Controlled sync with a model config

Export the ready cache revisions into an editable TOML file, enable only the
models to publish, and sync that exact selection:

```sh
mstore config export
$EDITOR models.toml
mstore config check models.toml
mstore sync --config models.toml --dry-run
mstore sync --config models.toml
```

Without `--output`, export writes `./models.toml` and refuses to replace an
existing file; use `--overwrite` only when replacement is intended. The export
contains every ready revision with `enabled = false`; an omitted `enabled` value
is also false. Each enabled entry must use a full
`provider:repo@revision` source and may choose a destination `name`:

```toml
schema = 1

[defaults]
hash = false

[[models]]
source = "hf:Qwen/Qwen3-TTS-12Hz-1.7B-CustomVoice@COMMIT"
enabled = true
name = "qwen3-tts"
```

`sync --config` imports only enabled, exact cache revisions. A selected source
that is missing or incomplete causes a non-zero exit status. The configuration
does not control activation; use `mstore sync --config models.toml --activate`
when activation is wanted. The v1 import unit is a complete provider snapshot:
when multiple quantized files live in one snapshot, they are published together.
Different quantization repos or revisions can be given separate names such as
`model-q4-k-m` and `model-q8-0`.

Inspect, activate, and verify:

```sh
mstore list --versions --source
mstore show qwen3-tts-12hz-1-7b-customvoice --files
mstore activate qwen3-tts-12hz-1-7b-customvoice@7c4e61a29d8f
mstore verify --full --record qwen3-tts-12hz-1-7b-customvoice
```

`path` prints only the resolved physical directory to stdout, which makes it
safe for command substitution:

```sh
docker run --rm \
  -v "$(mstore path qwen3-tts-12hz-1-7b-customvoice):/m:ro" \
  IMAGE
```

Use `--link` when the logical `current` path is desired instead.

Copy through any local or mounted filesystem:

```sh
mstore copy --to /mnt/models --all --current-only --verify full
mstore copy --to /mnt/backup --all --all-versions
```

There is intentionally no SSH, SFTP, or object-storage transport. Mount that
storage first and pass its path.

Generate a download script instead of copying large model files to another
machine:

```sh
mstore generate --all > download-models.sh
bash download-models.sh
```

To recreate an enabled model configuration on another machine without a local
mstore store, generate directly from the config:

```sh
mstore generate --config models.toml > download-models.sh
bash download-models.sh
```

Use `--uv` when the destination machine should run the provider CLIs through
uv, and combine it with `--hf-mirror` to route Hugging Face downloads through
HF-Mirror:

```sh
mstore generate --uv --hf-mirror --all > download-models.sh
```

The generated Bash script uses each manifest's recorded provider, repository,
and revision: `hf download REPO --revision REVISION` for Hugging Face and
`modelscope download --model REPO --revision REVISION` for ModelScope. Install
the relevant provider CLIs first and authenticate before downloading private or
gated models. With `--uv`, the script uses `uvx --from huggingface_hub hf` and `uvx modelscope`
instead, so only uv needs to be installed. `--hf-mirror` prefixes only Hugging
Face commands with `HF_ENDPOINT=https://hf-mirror.com`. `--all` includes every
published version; use
`--current-only` to export only active versions. Explicit `model` or
`model@version` arguments are also accepted; a bare model name resolves its
`current` version. `gen` is available as a short alias. Identical
provider/repository/revision combinations are emitted once. After each source
download, the script runs a source-specific `mstore import` with the recorded
name and version, preserving aliases and avoiding unrelated ready caches on the
destination. Active versions are imported with `--activate`. `mstore` must be
available on `PATH`. Set `MSTORE_STORE=/destination` when the target store is
not the default. Non-commit ModelScope revisions are marked with a warning
because they may move before the script runs. Provider download commands include
the published file inventory so partial snapshots are not expanded. If aliases
share a source but have different inventories, generation stops rather than
silently publishing one alias with another alias's files.
Each source uses an isolated persistent provider cache under
`${MSTORE_DOWNLOAD_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/mstore/downloads}`.
Directories are keyed by the exact provider, repository, revision, and selected
file inventory, so a repeated script can resume or reuse only the matching
source cache. Set
`MSTORE_DOWNLOAD_CACHE` to use another mstore-owned root. Versions imported
with recorded hashes are re-imported with `--hash` so full verification remains
available on the destination.
For older manifests without a recorded inventory, the script warns and downloads
the full revision instead of pretending a selective reconstruction is exact.
When generated from `--config`, the script downloads each enabled source's full
configured revision, imports it with the configured name, and does not activate
it. Config files have no published file inventory, so this can download more
than a script generated from stored manifests. `defaults.hash = true` adds
`--hash` to those imports.
`--json` returns the selected models and generated script as a single JSON value.

Maintenance commands:

```sh
mstore rename old-name new-name --dry-run
mstore rm --yes model@version
mstore rm --yes 'hf:namespace/repo@revision'
mstore rm --inactive --yes model
mstore gc --older-than 24h --dry-run
mstore prune --dry-run
mstore prune --yes
mstore doctor --provider all --write-test
mstore cache path
mstore cache clean --yes
mstore cache clean --path /srv/mstore-downloads --yes
```

`rm` is an alias for `remove`. It accepts either `model@version` or a complete
`hf:namespace/repo@revision` / `ms:namespace/repo@revision` source reference. The
source form resolves the published version from its manifest, so callers do not
need to normalize the repository name or remember the 12-character version
directory prefix. The source revision may be complete or a uniquely matching
prefix. If one source was imported under multiple aliases, the command reports
the candidate `model@version` references and requires an explicit choice. Remove
options may appear before or after the model reference. `remove` protects the active version unless `--force` is explicit. `gc` only
cleans staging data, `.part` files, and stale locks; it never deletes published
models or provider caches. `cache clean` is opt-in and removes only a cache root
carrying mstore's ownership marker; it rejects unsafe locations and never
deletes Hugging Face or ModelScope provider-wide caches. `prune` defaults to a
dry-run across both providers and covers `incomplete`, `invalid`, and `conflict`
entries. Only `prune --yes` removes precisely revalidated provider repositories
or snapshots; ready sources (including ready name conflicts), imported entries,
active provider revisions, locked targets, and published mstore versions are
protected.

## CLI reference

Top-level commands:

```text
scan import sync config cache generate list(ls) show path activate rename verify
copy(cp) remove(rm) gc prune doctor completion help
```

Global options must precede the command:

```text
--store PATH  --json  -q/--quiet  -v/-vv/--verbose
--no-color  -h/--help  -V/--version
```

Provider references use `hf:namespace/repo[@revision]` or
`ms:namespace/repo[@revision]`. Store references use `model[@version]`.

Important command options:

- `scan`: `--provider`, `--ready-only`, `--new-only`, `--long`
- `import`: `--name`, `--version`, `--activate`, `--hash`, `--jobs`, `--dry-run`
- `sync`: `--provider`, `--config`, `--activate`, `--hash`, `--jobs`, `--dry-run`
- `config export`: `--output`, `--provider`, `--overwrite`
- `config check`: `FILE`
- `cache clean`: `--path PATH`, `--yes`
- `generate` (`gen` alias): model refs or `--all`; `--current-only` with `--all`;
  `--uv`; `--hf-mirror`
- `list`: `--versions`, `--source`, `--long`
- `show`: `--files`, `--hashes`
- `path`: `--link`
- `activate`: `--no-verify`
- `rename`: `--dry-run`
- `verify`: `--all`, `--full`, `--record`, `--jobs`
- `copy`: `--to`, `--all`, `--all-versions`, `--current-only`,
  `--verify none|quick|full`, `--jobs`, `--dry-run`
- `remove` / `rm`: `model@version` or a provider source reference; `--inactive`,
  `--all-versions`, `--force`, `--yes`, `--dry-run`
- `gc`: `--older-than`, `--dry-run`
- `prune`: `--provider hf|ms|all`, `--status incomplete,invalid,conflict`,
  `--dry-run`, `--yes`, `--force`, `--json`
- `doctor`: `--provider`, `--write-test`
- `completion`: `bash`, `zsh`, `fish`, or `powershell`

`--json` emits one JSON value on stdout. Diagnostics go to stderr. Stable exit
codes are: `2` invalid arguments, `3` cache/source problems, `4` conflicts,
`5` verification failures, `6` lock timeouts, and `7` insufficient space;
other runtime failures use `1`.

## Scope

mstore does not download, convert, merge, quantize, infer with, or evaluate
models itself. It can generate provider CLI download scripts, but it has no
web UI, registry server, database, or remote transport.

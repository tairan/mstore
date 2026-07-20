# mstore

**English** | [简体中文](README.zh-CN.md)

`mstore` publishes models that are already present in the native Hugging Face
or ModelScope cache into a portable, immutable local model store. It never
downloads models and never writes to provider caches.

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

Only `models/<namespace>/<repo>` is accepted. Each model must have a valid
`.mv`; both a plain revision and ModelScope's
`Revision:<revision>,CreatedAt:<time>` representation are understood.
Historical `hub/<namespace>/<repo>` layouts are deliberately rejected as
unsupported rather than probed or guessed.

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
mstore import --activate hf:Qwen/Qwen3-TTS-12Hz-1.7B-CustomVoice@COMMIT
mstore import --all-new --provider ms
mstore sync --activate
```

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

Maintenance commands:

```sh
mstore rename old-name new-name --dry-run
mstore remove model@version --yes
mstore remove model --inactive --yes
mstore gc --older-than 24h --dry-run
mstore doctor --provider all --write-test
```

`remove` protects the active version unless `--force` is explicit. `gc` only
cleans staging data, `.part` files, and stale locks; it never deletes published
models or provider caches.

## CLI reference

Top-level commands:

```text
scan import sync list(ls) show path activate rename verify
copy(cp) remove(rm) gc doctor completion help
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
- `import`: `--all-new`, `--name`, `--provider`, `--activate`, `--hash`,
  `--jobs`, `--dry-run`
- `sync`: `--activate`, `--hash`, `--jobs`, `--dry-run`
- `list`: `--versions`, `--source`, `--long`
- `show`: `--files`, `--hashes`
- `path`: `--link`
- `activate`: `--no-verify`
- `rename`: `--dry-run`
- `verify`: `--all`, `--full`, `--record`, `--jobs`
- `copy`: `--to`, `--all`, `--all-versions`, `--current-only`,
  `--verify none|quick|full`, `--jobs`, `--dry-run`
- `remove`: `--inactive`, `--all-versions`, `--force`, `--yes`, `--dry-run`
- `gc`: `--older-than`, `--dry-run`
- `doctor`: `--provider`, `--write-test`
- `completion`: `bash`, `zsh`, `fish`, or `powershell`

`--json` emits one JSON value on stdout. Diagnostics go to stderr. Stable exit
codes are: `2` invalid arguments, `3` cache/source problems, `4` conflicts,
`5` verification failures, `6` lock timeouts, and `7` insufficient space;
other runtime failures use `1`.

## Scope

mstore does not download, convert, merge, quantize, infer with, or evaluate
models. It has no web UI, registry server, database, or remote transport.

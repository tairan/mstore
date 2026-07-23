# mstore

[English](README.md) | **简体中文**

[![CI](https://github.com/tairan/mstore/actions/workflows/ci.yml/badge.svg)](https://github.com/tairan/mstore/actions/workflows/ci.yml)

`mstore` 将 Hugging Face 或 ModelScope 原生缓存中已经下载完成的模型发布到
便携、不可变的本地模型仓库。它自身不会下载模型，也不会写入 provider 缓存，
但可以根据已发布模型的 manifest 生成 provider 下载脚本。

发布后的目录只包含普通文件，因此可以只读挂载到容器、复制到已挂载磁盘、
进行备份，或在没有数据库的情况下迁移到其他机器。

## 安装与构建

仓库使用 [mise](https://mise.jdx.dev/) 固定 Go 工具链：

```sh
mise install
mise run check
mise run build
```

当前平台的静态二进制写入 `bin/mstore`。`mise run build-all` 可复现地生成
`dist/mstore-linux-amd64`。项目不支持 ARM64 构建。构建时设置
`CGO_ENABLED=0`；mstore 不依赖 Python、rsync、数据库、常驻 daemon 或 CGO
运行时。

可用任务包括 `fmt`、`lint`、`test`、`check`、`build` 和 `build-all`。

## 持续集成与发布

GitHub Actions 会在 pull request、推送到 `main` 以及手动触发时运行
`mise run check`，并构建静态 Linux amd64 二进制。

推送严格遵循 [SemVer](https://semver.org/lang/zh-CN/) 的标签即可创建
GitHub Release：

```sh
git tag v1.2.3
git push origin v1.2.3
```

`v1.2.3-rc.1` 等预发布标签会创建 prerelease。每个 Release 包含
`mstore-linux-amd64` 和 `mstore-linux-amd64.sha256`，并将版本号写入
二进制。本地构建时也可以通过 `MSTORE_VERSION` 设置内嵌版本：

```sh
MSTORE_VERSION=v1.2.3 mise run build-all
```

## Provider 缓存

Hugging Face 缓存按以下顺序查找：

1. `HF_HUB_CACHE`
2. `$HF_HOME/hub`
3. `~/.cache/huggingface/hub`

支持标准的 `models--namespace--repo/{blobs,snapshots,refs}` 布局。复制
snapshot 时会跟随符号链接，发布后的模型只包含普通文件。悬空链接以及不完整
或临时文件会被拒绝。

ModelScope 缓存按以下顺序查找：

1. `$MODELSCOPE_CACHE/models`
2. `~/.cache/modelscope/hub/models`

仅接受 `models/<namespace>/<repo>` 布局。每个模型必须包含有效的 `.mv`；
既支持纯 revision，也支持 ModelScope 的
`Revision:<revision>,CreatedAt:<time>` 表示形式。历史
`hub/<namespace>/<repo>` 布局会被明确判定为不支持，不会进行探测或猜测。
ModelScope 会将缓存目录名中的 `.` 编码为 `___`；mstore 会在展示的 source ID、
manifest 和生成的下载命令中还原为 `.`。

## 仓库布局

默认模型仓库为 `${MSTORE_HOME:-~/models}`，可通过 `--store PATH` 覆盖：

```text
<store>/
├── <model-key>/
│   ├── <version>/
│   │   ├── ...模型文件...
│   │   └── .mstore.json
│   └── current -> <version>
├── .stage/
└── .locks/
```

模型 key 由仓库 basename 机械规范化生成：转换为小写，将空格和下划线替换
为 `-`，并合并重复分隔符。key 必须为 ASCII，最长 64 字节，绝不会被静默
截断。发生名称冲突时使用 `--name` 显式消歧。version 默认取完整 revision
的前 12 字节；前缀冲突时自动扩展。manifest 会保留完整身份信息：provider、
仓库和 revision。

导入过程使用模型级锁、确定性的 staging 目录、可续传的 `.part` 文件、
fsync、复制前后的源文件树扫描、staging 校验，以及同一文件系统内的原子
rename。已存在且身份一致的版本会直接跳过。已发布的 version 目录不会被覆盖。

## 常见用法

扫描原生缓存并发布已完成的 revision：

```sh
mstore scan --provider all --long
mstore sync --dry-run
mstore sync
mstore import --activate hf:Qwen/Qwen3-TTS-12Hz-1.7B-CustomVoice@COMMIT
mstore sync --activate qwen3-tts-12hz-1-7b-customvoice
```

不指定模型参数时，`sync` 会扫描所有 provider 并导入全部 ready revision，
包括从未导入过的仓库。可以用 `--provider hf|ms|all` 限定扫描范围。不存在的
provider 缓存会被跳过，incomplete revision 会被忽略；单个模型失败不会阻止
其他模型继续同步。名称冲突会直接失败，不会任意选择归属。

只有显式指定 `--activate`，`sync` 才会修改 `current`。启用激活后，
Hugging Face 按 `refs/main`、`refs/master` 的顺序选择，ModelScope 优先使用
`.mv`；如果一个仓库只有一个 ready revision，则将其作为兜底选择。

查看、激活和校验：

```sh
mstore list --versions --source
mstore show qwen3-tts-12hz-1-7b-customvoice --files
mstore activate qwen3-tts-12hz-1-7b-customvoice@7c4e61a29d8f
mstore verify --full --record qwen3-tts-12hz-1-7b-customvoice
```

`path` 只向 stdout 输出解析后的物理目录，因此可以安全地用于命令替换：

```sh
docker run --rm \
  -v "$(mstore path qwen3-tts-12hz-1-7b-customvoice):/m:ro" \
  IMAGE
```

需要逻辑 `current` 路径时可使用 `--link`。

通过任意本地或已挂载文件系统复制：

```sh
mstore copy --to /mnt/models --all --current-only --verify full
mstore copy --to /mnt/backup --all --all-versions
```

项目有意不实现 SSH、SFTP 或对象存储传输。请先挂载相应存储，再传入其路径。

当复制大模型到另一台机器过慢时，可以根据当前仓库生成下载脚本：

```sh
mstore generate --all > download-models.sh
bash download-models.sh
mstore sync
```

目标机器希望通过 uv 运行 provider CLI 时可使用 `--uv`；与 `--hf-mirror`
组合可让 Hugging Face 下载走 HF-Mirror：

```sh
mstore generate --uv --hf-mirror --all > download-models.sh
```

生成的 Bash 脚本会使用 manifest 中记录的 provider、仓库和 revision：Hugging
Face 使用 `hf download REPO --revision REVISION`，ModelScope 使用
`modelscope download --model REPO --revision REVISION`。执行前请安装相应的
provider CLI；私有或受限模型还需要先完成认证。使用 `--uv` 时，脚本改为
调用 `uvx hf` 与 `uvx modelscope`，目标机器只需安装 uv。`--hf-mirror` 仅为
Hugging Face 命令添加 `HF_ENDPOINT=https://hf-mirror.com`。`--all` 包含所有
已发布版本，
可配合 `--current-only` 只导出当前激活版本。也可以传入明确的 `model` 或
`model@version`；不带 version 的模型名会解析为 `current`。
`gen` 可作为短别名使用。相同的
provider、仓库和 revision 只会生成一条下载命令。`--json` 会输出所选模型和
生成脚本组成的一个 JSON 值。

维护命令：

```sh
mstore rename old-name new-name --dry-run
mstore remove model@version --yes
mstore remove model --inactive --yes
mstore gc --older-than 24h --dry-run
mstore doctor --provider all --write-test
```

除非显式指定 `--force`，`remove` 会保护当前激活版本。`gc` 只清理 staging
数据、`.part` 文件和失效锁；它不会删除已发布模型或 provider 缓存。

## CLI 参考

顶层命令：

```text
scan import sync generate list(ls) show path activate rename verify
copy(cp) remove(rm) gc doctor completion help
```

全局参数必须位于命令之前：

```text
--store PATH  --json  -q/--quiet  -v/-vv/--verbose
--no-color  -h/--help  -V/--version
```

Provider 引用使用 `hf:namespace/repo[@revision]` 或
`ms:namespace/repo[@revision]`；模型仓库引用使用 `model[@version]`。

主要命令参数：

- `scan`：`--provider`、`--ready-only`、`--new-only`、`--long`
- `import`：`--name`、`--activate`、`--hash`、`--jobs`、`--dry-run`
- `sync`：`--provider`、`--activate`、`--hash`、`--jobs`、`--dry-run`
- `generate`（别名 `gen`）：模型引用或 `--all`；搭配 `--all` 可使用
  `--current-only`；`--uv`；`--hf-mirror`
- `list`：`--versions`、`--source`、`--long`
- `show`：`--files`、`--hashes`
- `path`：`--link`
- `activate`：`--no-verify`
- `rename`：`--dry-run`
- `verify`：`--all`、`--full`、`--record`、`--jobs`
- `copy`：`--to`、`--all`、`--all-versions`、`--current-only`、
  `--verify none|quick|full`、`--jobs`、`--dry-run`
- `remove`：`--inactive`、`--all-versions`、`--force`、`--yes`、
  `--dry-run`
- `gc`：`--older-than`、`--dry-run`
- `doctor`：`--provider`、`--write-test`
- `completion`：`bash`、`zsh`、`fish` 或 `powershell`

`--json` 会向 stdout 输出一个 JSON 值，诊断信息写入 stderr。稳定退出码为：
`2` 表示参数错误，`3` 表示缓存或源错误，`4` 表示冲突，`5` 表示校验失败，
`6` 表示锁超时，`7` 表示磁盘空间不足；其他运行时错误使用 `1`。

## 功能边界

mstore 自身不负责下载、转换、合并、量化、推理或评测模型。它可以生成调用
provider CLI 的下载脚本，但不提供 Web UI、registry 服务、数据库或远程传输。

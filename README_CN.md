# opencode-free —— 让 CLIProxyAPI 能用 OpenCode Zen 免费模型

## 问题

在 CPA 里把 OpenCode 配成 `api-keys.openai-compatibility` 供应商，请求会拿到：

```
HTTP 403  FreeTierError
```

原因不是 header 缺失，也不是 URL 写错。免费层的闸门检查的是**请求体的内容**，要求请求看起来像一次 OpenCode agent 会话。CPA 的供应商配置能加任意 header，但**没有注入 body 的能力**——所以这件事只能靠插件。

## 闸门配方（2026-10-15 直接对 `https://opencode.ai/inference` 实测）

测试模型 `mimo-v2.6-flash-free`。用消融法逐项验证：

**必需：**

| 项 | 值 |
|---|---|
| `Authorization` | `Bearer public`（字面量，区分大小写） |
| `User-Agent` | `opencode/latest/<ver>/cli` |
| `x-opencode-client` | `cli` |
| `x-opencode-session` 与 `x-opencode-session-id` | 相同，形如 `ses_[0-9a-f]{12}[0-9A-Za-z]{14}` |
| `stream` | 必须为 `true`——`false` 或省略都 403，上游一律回 SSE |
| `tools` | 必须含**官方原版** `read` 定义 |

**不需要：** `stream_options`、`x-opencode-project`、`x-session-affinity`、`X-Session-Id`。

**几个容易踩的点：**

- `/v1/models` **不受闸门保护**，裸请求也回 200——拿它验证"通了没有"是无效的，必须打推理端点。
- 闸门**按模型区分**：免费模型走指纹检查，付费模型（`glm-5.3` 等）返回的是 401 `AuthError`。
- 名字对不够，定义必须**逐字节**是官方那份。带正确名字的桩定义一样 403。
- 12 个已识别名字里，**单独发只有 `read` 能过**；但 `read` + 任意未知工具可以过。
- session id 可以是**常量**，跨请求复用即可。per-conversation 铸造只影响 prompt cache 亲和性，与过闸无关。

## 插件如何判断"这是要改的请求"

**按凭据判断**——`Metadata["selected_auth_id"]` 里带着供应商身份：

```
openai-compatibility:opencode:6df7070a759c
└────────类型───────┘└─组名─┘└─密钥哈希─┘
```

中间那段就是你在 `api-keys.openai-compatibility` 里给这个组起的 `name:`。所以配置里**直接写组名**，单选多选都行：

```yaml
providers: opencode                      # 单选

providers:                               # 多选
  - opencode
  - openrouter
```

**没选的供应商原样放行，一个字节都不动。** 插件对 CPA 服务的所有供应商都生效，所以这是必填项。

`providers` 里写了但宿主配置里没有的组名，插件会**拒绝注册**并列出可选项：

```
plugin.register failed: opencode-free: unknown provider(s) opencod;
the host serves: opencode, some-other-endpoint
```

### 组名是怎么"发现"的

插件在注册时读**宿主自己的配置文件**，把 `openai-compatibility` 下每个组的 `name:` 列出来（`disabled: true` 的跳过），填进 `ConfigField.EnumValues`，管理界面可以据此渲染选择器；同时用来校验你写的组名。

配置文件路径来自进程自己的 argv（`/proc/self/cmdline` 里的 `-config` / `--config`，支持空格和 `=` 两种写法），和 CPA 自己解析 `-config` 的方式一致；没有就回落到工作目录下的 `config.yaml`。只解析组名，配置里的密钥不会被读取或记录。

> **为什么不用 `host.auth.list`。** 这是宿主唯一一个列凭据的回调，本来最合适，但实测它**不返回 `api-keys` 派生的凭据**：`buildHostAuthFileEntry` 会把既没有 `path` 又没有 `runtime_only` 属性的条目丢掉，而配置合成的 auth 两者都没有（`synthesizer/config.go` 只设 `source`/`base_url`/`compat_name`/`provider_key`/`config_index`）。实测：配置里有一个 openai-compatibility 组，回调返回 `{"files":[]}`。所以改读配置文件。

发现失败（比如配置路径解析不到）不影响使用：插件打一条 warn，然后**按你写的名字直接用**，不校验。

### 为什么不用别的字段

排查时试过几种，都不行，记在这里省得再走一遍：

- **`Headers` 不行。** 它装的是**客户端**发来的请求头。CPA 在 `conductor_execution.go:342` 传的是 `cloneRequestHeaders(opts.Headers)`，而供应商配置里的 `x-opencode-client` 这类自定义头，是在**更晚**的 executor 阶段才由 `ApplyCustomHeadersFromAttrs` 加上的——时间上根本看不到。
- **`ToFormat` 不行。** openai-compatibility 供应商的值是 `openai`，任何 OpenAI 兼容上游都一样。
- **模型名能用，但不如凭据准。** 它是间接证据，而且 `models` 得跟着供应商的模型列表走。

`selected_auth_id` 能用，是因为 `publishSelectedAuthMetadata`（`conductor_execution.go:524`）在 `applyRequestAfterAuthInterceptor`（同文件 588 行）**之前**执行。

### 过滤是必填的

`providers` 留空插件会**拒绝注册**并报错。因为这个钩子对 CPA 服务的**所有**供应商生效——没有过滤的话，Claude / Gemini / Codex 的请求也会被强制 `stream:true`，调用方自己的 `read`/`shell`/`edit` 工具定义还会被 OpenCode 的覆盖掉。静默改坏无关流量比启动失败难查得多。

`models` 是可选的二次收窄，写不写都行；两个都写时取交集。

排障用 `debug-log`，每次拦截追加一行 JSON，`decision` 会说明为什么没命中：

```json
{"decision":"rewrite","changed":true,"selected_auth_id":"openai-compatibility:opencode:6df7070a759c","model":"mimo-v2.6-flash-free","requested_model":"mimo-free","source_format":"openai","to_format":"openai","stream":false}
```

`decision` 的取值：`rewrite` / `skip:provider` / `skip:model` / `skip:not-registered`。

## 本插件做什么

声明 `request_interceptor` 一项能力，挂在 `request.intercept_after` 钩子上：

0. 凭据不属于 `providers` 里选中的组（或模型不匹配 `models`）→ **原样放行**
1. `stream != true` → 置为 `true`
2. `tools` 缺失或不含 `read` → 追加官方 `read` 定义
3. `tools` 里出现 12 个已识别名字中的任何一个 → 若定义与官方不一致，替换为官方定义
4. 其余工具**原样放行**，下游 function calling 不受影响

改写是**幂等**的：对已经合规的请求不做任何改动，不会重编码 JSON。

**只注入 `read`，不注入 opencode2api 那 5 个核心工具。** 理由是实测：`read` 单独就能过闸，而 5 个工具要 5735 字节。更重要的是，模型**真的会调用**给它的工具——注入 `shell`/`edit`/`write` 等于把每个请求都变成 agent 请求，而调用方可能只是想要一句话回答。少注入一个工具就少一条跑偏的路径。

**不做的（按决定刻意不做）：**

- 不改任何 header。指纹 header 由 CPA 配置里的 `headers:` 提供（见下）。
- 不碰 `Authorization`，由 CPA 的 `api-key: public` 提供。
- 不做响应侧处理。
- **不做 opencode2api 的 phantom guard**（`usableToolBlocks` + `finish_reason` 降级）。

## 已知风险（接受）

- **注入的 `read` 会真的被调用。** 带文件类提示词时，模型会返回 `finish_reason: tool_calls`、`tool_calls[0].function.name = "read"`、content 为空。`tool_choice: "none"` **无效**，上游忽略它。这个 `tool_use` 会作为下游客户端从未声明过的工具调用送达。
- **非流式下游请求会坏。** 上游强制 SSE，本插件不设保护，放行让它坏。
- **常量 session id 在高并发下是否被限流，未验证。**
- **闸门会变。** 2026-09-16 和 2026-10-14 都改过（后者加了逐字节定义比对）。工具定义不内嵌，是运行时获取的——删掉缓存（或重跑 `fetch-tools.sh`）再重启即可，不用重新编译。

## 安装

CPA 在 `<plugins.dir>/<goos>/<goarch>/` 和 `<plugins.dir>/` 里找插件，文件名必须是 `<插件ID>.so`（商店装的是 `<插件ID>-v<版本>.so`，加载时会剥掉版本后缀，ID 仍对得上）。`plugins.dir` 默认是 `plugins`。

**自己编译的话，在跑 CPA 的那台机器上编。** `.so` 链接宿主机的 libc，在新 glibc 上编出来的拿到老系统上加载会直接失败（实测编出的 `.so` 要求 `GLIBC_2.34`——`pthread_*` 和 `res_search` 是 Go 运行时用的——而 CentOS 7 只有 2.17、Ubuntu 20.04 只有 2.31）。所以不要把本地编的 `.so` 拷来拷去，只传源码；商店里那份是特意在 glibc 2.17 容器里编的，不受这条限制。

```sh
./install.sh                 # 编译 + 装到 ./plugins，并打印该填的配置
./install.sh -d /etc/cpa/plugins
./install.sh -n              # 只看会做什么，不落盘
```

工具定义不用管——插件首次启动会自己下载并缓存（见下文「工具定义数据」）。用 Makefile 也行：

```sh
make            # 编译出 opencode-free.so
make install    # 拷到 plugins/（PLUGIN_DIR=... 可改）
make vet test   # 检查
```

### CPA 配置

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    opencode-free:
      enabled: true
      priority: 100
      providers: opencode              # 必填，组名要和下面 api-keys 里的 name 对上
      # providers:                     # 多选就写成列表
      #   - opencode
      #   - openrouter
      # tools-file: "/etc/cpa/opencode_tools.json"   # 可选，见下文「工具定义数据」
      # tools-cache: "/var/cache/opencode-free/opencode_tools.json"
      # models:                        # 可选二次收窄，省略即该组下的所有模型
      #   - "mimo-*"
      # force-stream: true             # 默认 true
      # debug-log: "/tmp/opencode-free.log"

api-keys:
  openai-compatibility:
    - name: opencode
      base-url: https://opencode.ai/inference/v1
      headers:
        User-Agent: "opencode/latest/2.0.29/cli"
        x-opencode-client: "cli"
        x-opencode-session: "$CPA-SESSION-ID"
        x-opencode-session-id: "$CPA-SESSION-ID"
      keys:
        - api-key: "public"
      models:
        - name: mimo-v2.6-flash-free
          alias: mimo-free
```

`base-url` **要带 `/v1`**——CPA 只往后接 `/chat/completions`。写成 `https://opencode.ai/inference` 会得到上游 404，而 CPA 把它报成 `model_not_found`，看起来像模型没注册，很容易误判。

`$CPA-SESSION-ID` 由 CPA 展开为内部 session id。注意它**不一定**满足 `ses_` 形状——若不满足就写常量：

```yaml
        x-opencode-session: "ses_c70c671a8ffe559SNDjUqHfi7a"
        x-opencode-session-id: "ses_c70c671a8ffe559SNDjUqHfi7a"
```

时间部分（`ses_` 之后 12 位十六进制）需匹配 `^[0-9a-f]{9}f[c-f][0-9a-f]$`。

## 发布与安装

本仓库：**https://github.com/kakaw-1/opencode-free**（MIT）。**未上架官方注册表**，内置商店里搜不到——用下面「接自己的源」的方式装，或者从源码构建。

### 从插件商店装

把本仓库的注册表加成额外源，管理界面的商店列表里就会出现 OpenCode Free，装完即用，不需要 `fetch-tools.sh`、不需要手动配 `tools-file`——插件首次启动自己抓工具定义。

```yaml
plugins:
  store-sources:
    - "https://raw.githubusercontent.com/kakaw-1/opencode-free/main/registry.json"
```

安装走的是和官方商店完全相同的流程（下面「商店的约定」那几条都适用）。fork 的话把 URL 里的 `kakaw-1` 换成你自己。

商店的约定（我们的 CI 已经满足，改别人的插件时注意）：

| 项 | 要求 |
|---|---|
| release tag | 必须是 `v<version>`，且和 `main.go` 里的 `pluginVersion` 一致 |
| 每平台产物 | `<id>_<version>_<goos>_<goarch>.zip`，版本号不带 `v` |
| 校验 | 同一 release 里必须有 `checksums.txt`（sha256sum 格式） |
| zip 内部 | `<id>.so` 在**根目录**，不能嵌套、不能有第二个库 |

**商店装的是预编译 `.so`，它从不编译源码。** 所以 `release.yml` 在 `manylinux2014`（CentOS 7 / glibc 2.17）容器里构建，而不是在 runner 自己的新 glibc 上——`.so` 会通过 libc 解析 Go 运行时的 `pthread_*` 和 `res_search`，在新 glibc 上编出来的会要求 `GLIBC_2.34`，在 CentOS 7（2.17）和 Ubuntu 20.04（2.31）上直接加载失败。workflow 里有一条断言：`readelf` 出来的最高 glibc 版本符号超过 2.17 就让构建失败。

### 从源码构建安装

```sh
git clone https://github.com/kakaw-1/opencode-free.git
cd opencode-free
./install.sh -d /path/to/cpa/plugins
# 然后按它打印的提示改 CPA 配置，重启
```

需要 Go 1.26+ 和 gcc。目标机没有 Go 的话，两条路：装一个；或者在一台 **glibc 不比目标机新**的机器上交叉编译好 `.so` 再拷过去（`GOARCH=arm64 CC=aarch64-linux-gnu-gcc CGO_ENABLED=1 go build -buildmode=c-shared`）。想只传源码不打 git 的话，`make dist` 会打出 `opencode-free.tar.gz`，里面只有源码、两个脚本、`Makefile` 和文档，**没有 `.so`，也没有工具定义**。

### 发布（维护者）

改版本号 → 提交 → 打 tag，CI 会校验 tag 和 `pluginVersion` 一致，然后构建、打包、发 release：

```sh
# 改 main.go 里的 pluginVersion，比如 0.1.1
git commit -am "opencode-free: 0.1.1"
git tag v0.1.1 && git push origin main --tags
```

**商店不用再改注册表**：发新 release 就是发新版本。第一次上架才需要给 `CLIProxyAPI-Plugins-Store` 提 PR，加一条指向本仓库的条目。

`REPO_URL` 和 `AUTHOR` 编译期注入，不用改代码；默认值已经是本仓库，fork 的话覆盖：

```sh
make REPO_URL=https://github.com/<你>/opencode-free AUTHOR=<你>
```

CPA 会把这两项显示给运维看，并且**任一为空就拒绝注册**——所以别留空。`pluginRepoURL` 和 `pluginAuthor` 是 `var` 而非 `const`，就是给 `-ldflags -X` 留的口子。

`.gitignore` 排除了 `*.so` / `*.h` / `dist/` / `*.tar.gz` / `opencode_tools.json`——**别提交编译产物和那份定义**。

### 许可证边界

本仓库是 **MIT**，但 `opencode_tools.json` **不在里面**——它是 OpenCode 的数据，副本来自 GPL-3.0-or-later 的 `opencode2api`。`.gitignore` 已经把它排除了。

别顺手 `git add -f` 加回来，那会让整个仓库的许可变成一团浆糊。CI 里有一条断言守着这条线：在编出的 `.so` 里 grep 官方 `read` 的描述文本，命中就构建失败——所以就算有人重新内嵌，也会被挡住。

`LICENSE` 是纯净的 MIT 正文（多写一段说明会让 GitHub 判成 `NOASSERTION`，仓库页就不显示 MIT 了），范围说明单独放在 `NOTICE`。

### 关于供应商发现

插件靠读 CPA 的配置文件来发现供应商组名。配置文件路径取自进程 argv（`-config`/`--config`），找不到就回落到 `./config.yaml`。**这段只在 Linux 上有效**（依赖 `/proc/self/cmdline`）；其他系统会走回落，再失败就打一条 warn 并按你写的名字直接用，不影响工作。

## 构建要求

- Go 1.26+ 和 gcc（cgo + `-buildmode=c-shared`）
- 依赖 `github.com/router-for-me/CLIProxyAPI/v8`（go 会自动拉取）和 `gopkg.in/yaml.v3`
- 运行时需要一份工具定义：不配 `tools-file` 时插件自己下载并缓存，所以**没有额外前置步骤**；气隙环境用 `fetch-tools.sh` 备一份再用 `tools-file` 指过去

## 验证

```sh
go test ./...                              # 单元测试：注入 / 覆盖 / 幂等 / 放行 / 发现 / 配置解析
OPENCODE_LIVE=1 go test -run TestLive -v   # 打真实上游（会消耗免费额度）
```

live 测试需要先跑 `./fetch-tools.sh`；没有 `opencode_tools.json` 时它们会跳过。三项：对照组未改写的请求必须 403；插件改写的请求必须 200；客户端自带工具必须保留且整体 200。

**已在真实 CPA 上端到端验证过**（CPA 由源码编译，插件装进 `plugins.dir`；配置里放了两个 openai-compatibility 组，只有 `opencode` 被选中）：

| 场景 | 结果 |
|---|---|
| 插件 `enabled: false`，请求 `mimo-free` | `403 FreeTierError` |
| 插件 `enabled: true`，同一请求 | `200`，正常流式返回 |
| `providers: opencode`（标量），请求选中的 `mimo-free` | `rewrite`，`200` |
| 同上，请求**未选中**组的 `other-mini` | `skip:provider`，body 未改动 |
| `providers: [opencode, some-other-endpoint]`（列表），两者都请求 | 两条都 `rewrite` |
| `providers: opencod`（打错字） | 注册失败：`unknown provider(s) opencod; the host serves: opencode, some-other-endpoint` |
| 去掉 `tools-file`（首次启动） | 插件自行下载并缓存，注册成功，日志打印实际加载路径 |
| 去掉 `tools-file` 且断网、无缓存 | 注册失败，报错指名 `fetch-tools.sh` |
| 去掉 `tools-file` 且断网、**有**缓存 | 用缓存继续工作，只打一条 warn |
| `grep` 编出的 `.so` 找官方 `read` 描述 | 无命中（定义没被内嵌） |
| `POST /v0/management/plugin-store/opencode-free/install`（干净的 plugins 目录） | `status: installed`，落到 `plugins/linux/amd64/opencode-free-v0.1.0.so` |
| 装完不重启 | 插件热加载并注册成功，`restart_required: false` |
| 装出来的 `.so` 与 release 里的 amd64 zip 比对 sha256 | 一致（`5563f037…`） |
| 装完后打真实请求 | `200`，`decision: rewrite` |

即：403 确实由本插件消除，改写范围严格限定在选中的供应商内，且二进制里不含 GPL 数据。

插件注册时宿主会校验 `metadata`：`name`、`version`、`author`、`github_repository` **四项都必须非空**，否则只会打一条 `returned invalid metadata or no capabilities` 的 warn 然后静默不加载。本插件已填齐。

## 工具定义数据（不在本仓库里）

`opencode_tools.json` 是 OpenCode 官方 CLI 的 12 个工具定义（description + JSON Schema）。闸门做**逐字节**比对，所以必须是官方那份，名字对、内容不对一样 403。

**这份文件不随本仓库分发，插件也不内嵌它。** 原因：它的副本来自 `opencode2api`，而那个仓库是 **GPL-3.0-or-later**；本插件是 MIT。把文件放进仓库或编进 `.so`，等于在分发 GPL 数据，MIT 就名不副实了。

所以改成运行时获取。**默认插件自己去抓**，装完即用：

```yaml
# 什么都不用配。插件在注册时下载到缓存，默认
# ~/.cache/opencode-free/opencode_tools.json，可用 tools-cache 改。
```

抓取时校验 sha256（已知可用的那份是 `e481d200b9…`），不一致**只警告不报错**——上游更新过属于正常，为此拒绝启动比它想预防的 403 更糟。缓存用临时文件 + `rename` 原子替换，下载失败也不会留下半截文件让下次启动加载到垃圾。

已经抓过一次的机器，之后断网也能起来：刷新失败就用缓存，只打一条 warn。

气隙环境，或者想固定一份，就用脚本备好再指过去：

```sh
./fetch-tools.sh                       # 下载到 ./opencode_tools.json
./fetch-tools.sh -o /etc/cpa/opencode_tools.json
```

```yaml
tools-file: "/etc/cpa/opencode_tools.json"   # 配了就完全不碰网络
```

两者都没有、又下载不到，才会**拒绝注册**，报错指名 `fetch-tools.sh`。这个兜底是刻意的：宁可启动失败，也不要一个装上了却什么都不做的插件。

日志里会打印实际加载的路径，方便确认用的是缓存还是你指定的那份。

`.so` 里不含这些定义——CI 里有一条断言守着这条线（在二进制里 grep 官方 `read` 的描述，命中就失败）。

**闸门变过两次**（2026-09-16、2026-10-14）。又开始 403 的话，删掉缓存再重启（或重跑 `fetch-tools.sh`），不用重新编译。

本插件自身的代码是按协议行为重写的，未复制 `opencode2api` 的实现；MIT 只覆盖插件源码，见 `LICENSE`。

## 更省事的替代方案

如果只是想让 CPA 用上免费模型、且不介意多跑一个进程：直接跑 `opencode2api`，它是完整的 OpenAI 兼容网关，让 CPA 把它当普通 `openai-compatibility` 供应商指过去即可。零代码。写插件换来的是单二进制部署和更少的转发层。

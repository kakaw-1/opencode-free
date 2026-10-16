# opencode-free

A [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) plugin that makes **OpenCode Zen free-tier models** usable through CPA.

Full documentation is in Chinese: **[README_CN.md](README_CN.md)**. This page is the short version.

## The problem

Configure OpenCode as an `api-keys.openai-compatibility` provider in CPA and every request comes back:

```
HTTP 403  FreeTierError
```

The URL is right, and headers alone do not get you in. The gate wants both halves: the OpenCode headers (a CPA provider config supplies those) **and** a streaming body carrying OpenCode's official `read` tool definition, byte for byte. Ablated against the real upstream on 2026-10-15: headers without the tool definition → 403, tool definition without the headers → 403, both → 200. A CPA provider config has no way to touch the body, so a plugin is the only seam.

## What this does

Declares one capability (`request_interceptor`) on the `request.intercept_after` hook and rewrites the outbound Chat Completions body for the OpenAI-compatible groups you name:

0. Credential is not one of the selected `providers` (or the model does not match `models`) → **passed through untouched**
1. `stream != true` → set to `true`
2. `tools` missing or without `read` → append the official `read` definition
3. A `tools` entry shadows one of the 12 known OpenCode tool names but differs from the official definition → replaced
4. Every other tool is left alone, so downstream function calling still works

The rewrite is idempotent: a request that is already valid is not modified or re-encoded.

**Untouched is the default.** The hook fires for every provider CPA serves, so
anything not selected passes through byte for byte — without a selection,
Claude/Gemini/Codex traffic would get `stream: true` forced on it and its own
tool definitions overwritten. `providers` takes group names, or `*` for every
OpenAI-compatible group (the wildcard widens the group match, not the provider
type, so it still cannot reach Claude/Gemini/Codex credentials). Left empty the
plugin registers but rewrites nothing.

## Install

**From the CPA plugin store.** Not listed in the official registry, so the built-in
store does not carry it. Add this repo's registry as an extra source and install
through the store UI exactly as you would for any listed plugin:

```yaml
plugins:
  store-sources:
    - "https://raw.githubusercontent.com/kakaw-1/opencode-free/main/registry.json"
```

The install writes the prebuilt library into `plugins.dir`, enables the plugin
and hot-loads it — no restart. **One step is still yours: choosing the
providers.** The store cannot write plugin config (`install` takes only a
`version` query parameter), so a freshly installed plugin registers inert and
rewrites nothing until you set `providers` in the management UI, or:

Skip that step and the symptom is "the plugin is enabled and I still get 403":
`enabled: true` and `registered: true` in the plugin list, requests passed
through untouched, `403 FreeTierError` from upstream, and `skip:no-selection` as
the `decision` in the debug log.

```sh
curl -X PATCH -H 'Authorization: Bearer <management-key>' \
  -H 'Content-Type: application/json' \
  -d '{"providers":"opencode"}' \
  http://127.0.0.1:8317/v0/management/plugins/opencode-free/config
```

That persists the config and hot-reloads; no restart. Use `{"providers":"*"}`
to cover every OpenAI-compatible group.

**From source** — needs Go 1.26+ and gcc:

```sh
git clone https://github.com/kakaw-1/opencode-free.git
cd opencode-free
./install.sh -d /path/to/cpa/plugins
```

Then:

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    opencode-free:
      enabled: true
      priority: 100
      providers: opencode          # a name under api-keys.openai-compatibility, or "*" for all of them

api-keys:
  openai-compatibility:
    - name: opencode
      base-url: https://opencode.ai/inference/v1   # /v1 is required
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

Build on the machine that will run it, or take the prebuilt library from the releases (built against glibc 2.17 so it loads on older hosts).

`models` is the only thing CPA lists or routes on: `/v1/models` returns exactly
the names declared there, and neither CPA nor this plugin discovers upstream
models. Deleting the list took a working setup from 11 models to 0. Upstream
offered 11 free ones on 2026-10-15 — `cat-jam-free`,
`mimo-v2.6-flash-free`, `muse-spark-1.2-contributor-free`,
`muse-spark-1.3-contributor-free`, `nemotron-3.5-super-vl-free`,
`nemotron-3.5-lightning-free`, `jev-1.13-free`, `step-5-preview-free`,
`longcat-2.5-preview-free`, `deepseek-v4.1-flash-free`, `space-bunny-free` —
and they are not interchangeable: three of them (`muse-spark-1.2-contributor-free`,
`muse-spark-1.3-contributor-free`, `jev-1.13-free`) answer `400
ModelProtocolUnsupported` on `/v1/chat/completions` because they only speak their
native protocol, and the rest are rate-limited in per-model pools (`429
FreeUsageLimitError`). Neither is a rewrite failure; the debug log's `decision`
and `changed` say whether the plugin did its part.

## The tool definitions are not in this repo

The gate compares the `read` definition byte for byte, so the plugin has to send the official one. That data is OpenCode's: the copy this was verified against came from [opencode2api](https://github.com/jasonxu114514/opencode2api), which is **GPL-3.0-or-later**, and this plugin is **MIT**. Bundling it would make the `.so` a redistribution of GPL data.

So the plugin downloads and caches it at registration instead — one fetch, reused for a day, falling back to the cached copy when offline. Point `tools-file` at a local copy (`./fetch-tools.sh` writes one) to skip the network entirely. CI asserts the binary contains no tool definitions.

## Status

Reverse engineered and verified end to end against real CPA and the real upstream on 2026-10-15. The gate is not a documented contract and has changed twice already (2026-09-16, 2026-10-14) — if requests start returning 403, delete the cached catalogue and restart; no rebuild needed.

One red herring, because it is the first thing anyone clicks: the management
panel's per-provider **Test** button always fails for this provider. It posts to
`/v0/management/api-call` on the management plane, so the interceptor never
runs, and the body it sends (`stream: false`, no `tools`) fails both halves of
the gate on its own. `403 FreeTierError` there is expected, not a broken
install. The only meaningful check is a real `/v1/chat/completions` request plus
the `decision` field in the debug log.

The one accepted cost is the injected `read` itself: prompts that ask for a file
make the model call it (17/17 measured across four free models; plain chat
prompts never did, 7/7), and the caller gets a tool call for a tool it never
declared. Stripping it out of the response was measured and rejected — in 14 of
those 17 the reply was empty anyway, so stripping would have handed the caller
the same empty answer for the price of a per-chunk hot-path hook. `tool_choice:
"none"` is not a substitute either: `cat-jam-free` and
`nemotron-3.5-lightning-free` honour it, `step-5-preview-free` ignores it
outright.

The store path is verified too, not just the source build: the published
artifact was installed through CPA's own
`POST /v0/management/plugin-store/:id/install` into a clean plugin directory,
hot-loaded without a restart, and served a real request with `HTTP 200`. The
installed `.so` hashes identically to the released zip.

Concurrency was measured with six real agents (Codex CLI, `wire_api=responses`
through CPA's `/v1/responses`) sharing one constant `x-opencode-session`: 18
requests across three waves, all `200`, no `429`, latency flat at 1.35–3.18s and
faster in the last wave than the first. A serial control of the same six prompts
had a worse tail (up to 37s). The plugin rewrote every one of them — the debug
log grew by exactly the request count. Asking those agents for a file is where
the injected `read` shows up: 4 of 6 hit `unsupported call: read` before falling
back to their own shell, which turned 6 requests into 16 and 11s into 15–25s.

That check also found the reason `v0.1.1` exists: a store install writes the
library and enables the plugin but cannot write `providers`, and `v0.1.0`
refused to register without it. The install reported success while the plugin
never loaded. `v0.1.1` registers inert instead, and a single `PATCH` on
`/v0/management/plugins/opencode-free/config` brings it to life without a
restart.

MIT — see [LICENSE](LICENSE). [NOTICE](NOTICE) records the scope: the license covers the plugin's source, not the OpenCode tool definitions it fetches at runtime.

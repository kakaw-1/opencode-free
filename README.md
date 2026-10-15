# opencode-free

A [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) plugin that makes **OpenCode Zen free-tier models** usable through CPA.

Full documentation is in Chinese: **[README_CN.md](README_CN.md)**. This page is the short version.

## The problem

Configure OpenCode as an `api-keys.openai-compatibility` provider in CPA and every request comes back:

```
HTTP 403  FreeTierError
```

Nothing is missing from the headers and the URL is right. The free tier gates on the **request body**: it wants a streaming request carrying OpenCode's official `read` tool definition, byte for byte. A CPA provider config can inject arbitrary headers but has no way to touch the body — so a plugin is the only seam.

## What this does

Declares one capability (`request_interceptor`) on the `request.intercept_after` hook and rewrites the outbound Chat Completions body for the OpenAI-compatible groups you name:

0. Credential is not one of the selected `providers` (or the model does not match `models`) → **passed through untouched**
1. `stream != true` → set to `true`
2. `tools` missing or without `read` → append the official `read` definition
3. A `tools` entry shadows one of the 12 known OpenCode tool names but differs from the official definition → replaced
4. Every other tool is left alone, so downstream function calling still works

The rewrite is idempotent: a request that is already valid is not modified or re-encoded.

**Untouched is the default.** The hook fires for every provider CPA serves, so `providers` is required and anything not selected passes through byte for byte — without it, Claude/Gemini/Codex traffic would get `stream: true` forced on it and its own tool definitions overwritten.

## Install

**From the CPA plugin store.** Built in, nothing to fetch by hand.

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
      providers: opencode          # required; the name under api-keys.openai-compatibility

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

## The tool definitions are not in this repo

The gate compares the `read` definition byte for byte, so the plugin has to send the official one. That data is OpenCode's: the copy this was verified against came from [opencode2api](https://github.com/jasonxu114514/opencode2api), which is **GPL-3.0-or-later**, and this plugin is **MIT**. Bundling it would make the `.so` a redistribution of GPL data.

So the plugin downloads and caches it at registration instead — one fetch, reused for a day, falling back to the cached copy when offline. Point `tools-file` at a local copy (`./fetch-tools.sh` writes one) to skip the network entirely. CI asserts the binary contains no tool definitions.

## Status

Reverse engineered and verified end to end against real CPA and the real upstream on 2026-10-15. The gate is not a documented contract and has changed twice already (2026-09-16, 2026-10-14) — if requests start returning 403, delete the cached catalogue and restart; no rebuild needed.

MIT — see [LICENSE](LICENSE).

#!/bin/sh
# Build and install the opencode-free plugin into a CPA plugin directory.
#
#   ./install.sh                 # build, install into ./plugins
#   ./install.sh -d /etc/cpa/plugins
#   ./install.sh -n              # build and show what would happen, change nothing
#
# Build on the machine that will run it. A .so links against the host libc, so
# one built on a newer glibc will not load on an older one.
set -eu

plugin=opencode-free
plugin_dir=${PLUGIN_DIR:-plugins}
dry_run=0
go_bin=${GO:-go}
repo_url=${REPO_URL:-https://github.com/kakaw-1/opencode-free}
author=${AUTHOR:-kakaw-1}

usage() {
	cat <<'EOF'
usage: install.sh [-d plugin-dir] [-n]

  -d DIR   CPA plugin directory (default: $PLUGIN_DIR, else ./plugins)
  -n       dry run: build, print the config block, install nothing
  -h       this message

Environment: GO (default "go"), PLUGIN_DIR (default "plugins"),
             REPO_URL and AUTHOR (stamped into the binary; CPA shows them to
             operators and refuses to register the plugin if either is empty).
EOF
}

while [ $# -gt 0 ]; do
	case $1 in
	-d)
		[ $# -ge 2 ] || { echo "-d needs a directory" >&2; exit 2; }
		plugin_dir=$2
		shift 2
		;;
	-n | --dry-run)
		dry_run=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "unknown argument: $1" >&2
		usage >&2
		exit 2
		;;
	esac
done

cd "$(dirname "$0")"

# --- prerequisites -----------------------------------------------------------

if ! command -v "$go_bin" >/dev/null 2>&1; then
	echo "error: Go toolchain not found (looked for '$go_bin')." >&2
	echo "       Install Go 1.26+ and put it on PATH, or pass GO=/path/to/go." >&2
	exit 1
fi

# cgo needs a C compiler for the host platform.
if ! command -v cc >/dev/null 2>&1 && ! command -v gcc >/dev/null 2>&1; then
	echo "error: no C compiler found (need cc or gcc for cgo)." >&2
	exit 1
fi

echo "==> toolchain"
echo "    go:       $("$go_bin" version)"
echo "    platform: $(uname -s)/$(uname -m)"
echo "    libc:     $(ldd --version 2>/dev/null | head -1 || echo 'unknown')"

# --- build -------------------------------------------------------------------

echo "==> building $plugin.so"
CGO_ENABLED=1 "$go_bin" build -buildmode=c-shared \
	-ldflags "-X main.pluginRepoURL=$repo_url -X main.pluginAuthor=$author" \
	-o "$plugin.so" .
echo "    built $(pwd)/$plugin.so"
if [ "$repo_url" = "https://github.com/CHANGE-ME/opencode-free" ]; then
	echo "    note: REPO_URL is still the placeholder. Harmless (CPA only checks"
	echo "          that it is non-empty) but operators will see it."
fi

# --- install -----------------------------------------------------------------

echo
echo "==> install"
if [ "$dry_run" -eq 1 ]; then
	echo "    dry run: would install to $plugin_dir/$plugin.so"
else
	mkdir -p "$plugin_dir"
	install -m 0644 "$plugin.so" "$plugin_dir/$plugin.so"
	echo "    installed $plugin_dir/$plugin.so"
fi

# --- tool definitions --------------------------------------------------------

# The plugin injects OpenCode's official tool definitions. They are not bundled
# (GPL-3.0-or-later data; this plugin is MIT), so the plugin downloads and
# caches them on first start. Nothing to do here — fetch-tools.sh is still there
# for hosts that would rather pin a local copy.
echo
echo "==> tool definitions"
if [ -f opencode_tools.json ]; then
	echo "    local copy present: $(pwd)/opencode_tools.json"
	echo "    set tools-file to it to skip the plugin's own download"
else
	echo "    none here; the plugin downloads and caches them on first start"
fi

# --- next steps --------------------------------------------------------------

abs_plugin_dir=$(cd "$plugin_dir" 2>/dev/null && pwd || echo "$plugin_dir")

cat <<EOF

==> next: point CPA at it

1. Make sure plugins are enabled and dir matches where you installed:

   plugins:
     enabled: true
     dir: $abs_plugin_dir

2. Add the plugin config. 'providers' names the OpenAI-compatible groups to
   rewrite, exactly as spelled under api-keys.openai-compatibility[].name:

     configs:
       $plugin:
         enabled: true
         priority: 100
         providers: opencode
         # tools-file: "/etc/cpa/opencode_tools.json"   # optional, see below
         # models:
         #   - "mimo-*"
         # debug-log: "/tmp/$plugin.log"

   tools-file is optional. Left out, the plugin downloads the tool definitions
   on first start and caches them (~/.cache/$plugin/opencode_tools.json); set
   it to a local copy — ./fetch-tools.sh writes one — to skip the network
   entirely, which is what an air-gapped host wants.

3. Make sure the OpenCode provider itself is configured (base-url must end in
   /v1 — CPA appends only /chat/completions). See config.example.yaml.

4. Restart CPA and confirm it loaded:

     cpa -config /path/to/config.yaml
     # look for: pluginhost: plugin registered plugin_id=$plugin

   If registration failed the log says why. A wrong provider name is reported
   with the list of names actually found in your config.

Notes:
* The plugin discovers provider names by reading the CPA config file. It finds
  the path from the process argv (-config/--config), falling back to
  ./config.yaml. That lookup is Linux-only; elsewhere it relies on the fallback,
  and if it fails the plugin logs a warning and accepts the names you wrote.
* The tool definitions are OpenCode's data, not this plugin's, so they are not
  redistributed with it. If the free tier starts returning 403 again, delete the
  cached copy and restart — the gate has changed twice.
EOF

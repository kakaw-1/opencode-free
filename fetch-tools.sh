#!/bin/sh
# Download the OpenCode tool definitions the plugin injects.
#
#   ./fetch-tools.sh              -> ./opencode_tools.json
#   ./fetch-tools.sh -o /etc/cpa/opencode_tools.json
#
# WHY THIS IS A SEPARATE STEP
#
# The free-tier gate compares the "read" tool definition byte for byte, so the
# plugin has to send the official one — a stub with the right name still gets a
# 403. That data is not this plugin's: the copy this was developed against comes
# from opencode2api, which is GPL-3.0-or-later, and this plugin is MIT. Bundling
# the file would make the .so a distribution of GPL data, so it is fetched here
# instead, on your machine, for your own use.
#
# That also means the definitions are the one thing you may need to refresh: the
# gate changed on 2026-09-16 and again on 2026-10-14. Re-run this and restart CPA
# if requests start 403ing again.
set -eu

out=opencode_tools.json
url=${TOOLS_URL:-https://raw.githubusercontent.com/jasonxu114514/opencode2api/main/internal/gateway/opencode_tools.json}

# The definitions are byte-compared by the gate, so a known-good hash is worth
# checking. A mismatch is not fatal — upstream may simply have updated them —
# but it is worth knowing before you spend an hour debugging a 403.
known_sha256=e481d200b99ae522abfab65fa5a8f5c193df0e413b80671517f82cd2f4fbdeeb

usage() {
	cat <<'EOF'
usage: fetch-tools.sh [-o output-path] [-u url]

  -o PATH   where to write the catalogue (default: ./opencode_tools.json)
  -u URL    fetch from somewhere else (default: the opencode2api copy)
  -h        this message

Environment: TOOLS_URL (same as -u).
EOF
}

while [ $# -gt 0 ]; do
	case $1 in
	-o)
		[ $# -ge 2 ] || { echo "-o needs a path" >&2; exit 2; }
		out=$2
		shift 2
		;;
	-u)
		[ $# -ge 2 ] || { echo "-u needs a URL" >&2; exit 2; }
		url=$2
		shift 2
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

for tool in curl wget; do
	if command -v "$tool" >/dev/null 2>&1; then
		fetch=$tool
		break
	fi
done
if [ -z "${fetch:-}" ]; then
	echo "error: need curl or wget" >&2
	exit 1
fi

echo "==> fetching tool definitions"
echo "    from: $url"

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT INT TERM
if [ "$fetch" = curl ]; then
	curl -fsSL -o "$tmp" "$url"
else
	wget -qO "$tmp" "$url"
fi

# Sanity-check the shape before overwriting anything: the plugin injects the
# "read" entry and needs the parameters object to be there.
if ! grep -q '"read"' "$tmp"; then
	echo "error: fetched file does not look like a tool catalogue (no \"read\" entry)" >&2
	exit 1
fi

mkdir -p "$(dirname "$out")"
install -m 0644 "$tmp" "$out"

sum=$(sha256sum "$out" | cut -d' ' -f1)
echo "    wrote: $out"
echo "    sha256: $sum"
if [ "$sum" = "$known_sha256" ]; then
	echo "    matches the definitions this plugin was verified against."
else
	echo "    NOTE: differs from the verified set ($known_sha256)."
	echo "          Upstream may have updated them — that is fine if requests work,"
	echo "          and expected if they don't (the gate has changed before)."
fi

cat <<EOF

==> next: point the plugin at it

  plugins:
    configs:
      opencode-free:
        tools-file: "$(cd "$(dirname "$out")" && pwd)/$(basename "$out")"

Then restart CPA.
EOF

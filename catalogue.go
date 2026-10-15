package main

// Loading the tool catalogue.
//
// The catalogue is NOT embedded. The definitions are OpenCode's, and the copy
// this plugin was built against came from opencode2api, which is GPL-3.0-or-
// later; embedding them would put that data inside the .so and make this MIT
// repo's distribution anything but MIT. So the definitions are supplied at
// runtime, either from `tools-file` or from the cache the plugin fills itself
// (see fetch.go).
//
// This file only reads and validates. Deciding *which* file to read — and
// fetching one when the operator has not supplied it — is resolveCataloguePath's
// job, so that a plugin-store install needs no manual second step.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// parseCatalogue decodes a tool catalogue and checks the one entry that must be
// present. A catalogue without "read" is a configuration error, not something
// to paper over: without it every request would still 403.
func parseCatalogue(raw []byte, source string) (toolCatalogue, error) {
	defs := toolCatalogue{}
	if err := json.Unmarshal(raw, &defs); err != nil {
		return nil, fmt.Errorf("parse tool catalogue %s: %w", source, err)
	}
	if _, ok := defs[injectedTool]; !ok {
		return nil, fmt.Errorf("tool catalogue %s has no %q definition", source, injectedTool)
	}
	return defs, nil
}

// loadCatalogue reads the tool catalogue named by path.
//
// An empty path is an error, not a fallback: there is no embedded catalogue to
// fall back to. Callers reach this through resolveCataloguePath, which only
// returns empty if it could neither find nor fetch a catalogue — so this guard
// is mostly a backstop, and its message still names the fix.
func loadCatalogue(path string) (toolCatalogue, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("no tool catalogue available: `tools-file` is unset and none could be downloaded (the definitions are OpenCode's and are not redistributed here). Run ./fetch-tools.sh to download them, then set `tools-file` to the resulting path")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read tool catalogue %q: %w", path, err)
	}
	return parseCatalogue(raw, path)
}

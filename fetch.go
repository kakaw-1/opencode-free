package main

// Acquiring the tool catalogue at runtime.
//
// The catalogue is not embedded: the definitions are OpenCode's data, the copy
// this plugin was verified against came from opencode2api (GPL-3.0-or-later),
// and this plugin is MIT. Embedding them would make the .so a redistribution of
// GPL data. So they are fetched on the machine that runs the plugin, for that
// operator's own use — the same thing fetch-tools.sh does by hand, moved into
// the plugin so that installing from the plugin store works without a manual
// second step.
//
// Two ways to supply them:
//
//	tools-file: /etc/cpa/opencode_tools.json   explicit, never touches the network
//	(unset)                                    downloaded and cached automatically
//
// The explicit form wins when set, so an air-gapped host never waits on a
// timeout it cannot satisfy.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The same source fetch-tools.sh uses. Not this plugin's data — see above.
// A var so tests can point it at a local server.
var defaultToolsURL = "https://raw.githubusercontent.com/jasonxu114514/opencode2api/main/internal/gateway/opencode_tools.json"

const (
	// The catalogue this plugin was verified against. A mismatch is a warning,
	// not an error: upstream may simply have updated the definitions, and
	// refusing to start over that would be worse than the 403 it predicts.
	knownToolsSHA256 = "e481d200b99ae522abfab65fa5a8f5c193df0e413b80671517f82cd2f4fbdeeb"

	// The download is ~16 KB and happens at registration, not per request.
	toolsFetchTimeout = 20 * time.Second

	// The definitions are static and the gate changes rarely — twice in the year
	// to 2026-10. A copy younger than this is reused rather than re-fetched,
	// because the host calls plugin.reconfigure several times during a single
	// startup and there is no reason to hit the network once per call.
	catalogueRefreshInterval = 24 * time.Hour
)

// catalogueFetchMu serializes the check-and-refresh, so concurrent
// reconfigure calls cannot both decide to download.
var catalogueFetchMu sync.Mutex

// resolveCataloguePath decides which catalogue file to load.
//
// toolsFile, when set, is honoured as written: the operator named a path, so a
// missing file there is an error rather than an excuse to fetch something else.
// Otherwise the catalogue is cached under toolsCache (or a default cache
// directory) and re-fetched at most once a day, so a host that has fetched once
// keeps working offline and a single startup does not hit the network several
// times over.
func resolveCataloguePath(toolsFile, toolsCache string) (string, error) {
	toolsFile = strings.TrimSpace(toolsFile)
	if toolsFile != "" {
		return toolsFile, nil
	}
	cachePath, errCache := catalogueCachePath(toolsCache)
	if errCache != nil {
		return "", errCache
	}
	catalogueFetchMu.Lock()
	defer catalogueFetchMu.Unlock()
	if info, errStat := os.Stat(cachePath); errStat == nil && time.Since(info.ModTime()) < catalogueRefreshInterval {
		return cachePath, nil
	}
	if errFetch := refreshCatalogue(cachePath); errFetch != nil {
		// A stale copy beats refusing to start, but only if there is one.
		if _, errStat := os.Stat(cachePath); errStat == nil {
			logHost("warn", fmt.Sprintf("%s: could not refresh the tool catalogue (%v); using the cached copy at %s", pluginID, errFetch, cachePath))
			return cachePath, nil
		}
		return "", fmt.Errorf("%s: no `tools-file` configured and the tool catalogue could not be downloaded: %w. Set `tools-file` to a local copy (./fetch-tools.sh writes one), or fix network access to %s", pluginID, errFetch, defaultToolsURL)
	}
	return cachePath, nil
}

// catalogueCachePath picks where an automatically fetched catalogue lives.
func catalogueCachePath(toolsCache string) (string, error) {
	if trimmed := strings.TrimSpace(toolsCache); trimmed != "" {
		return trimmed, nil
	}
	base, errBase := os.UserCacheDir()
	if errBase != nil {
		return "", fmt.Errorf("%s: cannot determine a cache directory for the tool catalogue (%w); set `tools-cache` or `tools-file`", pluginID, errBase)
	}
	return filepath.Join(base, pluginID, "opencode_tools.json"), nil
}

// refreshCatalogue downloads the definitions to path, checking the shape and
// reporting (but not rejecting) a hash change. The file is replaced only after
// the download validates, so a failed refresh cannot leave a truncated
// catalogue behind for the next start to load.
func refreshCatalogue(path string) error {
	client := &http.Client{Timeout: toolsFetchTimeout}
	response, errGet := client.Get(defaultToolsURL)
	if errGet != nil {
		return fmt.Errorf("fetch %s: %w", defaultToolsURL, errGet)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch %s: HTTP %d", defaultToolsURL, response.StatusCode)
	}
	raw, errRead := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if errRead != nil {
		return fmt.Errorf("read %s: %w", defaultToolsURL, errRead)
	}
	// parseCatalogue is the same check registration applies, so a bad download
	// never reaches the cache.
	if _, errParse := parseCatalogue(raw, defaultToolsURL); errParse != nil {
		return errParse
	}
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != knownToolsSHA256 {
		logHost("warn", fmt.Sprintf("%s: the downloaded tool definitions differ from the set this plugin was verified against; expected if upstream updated them, and the likely cause if requests start returning 403", pluginID))
	}
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o755); errMkdir != nil {
		return fmt.Errorf("create catalogue cache directory: %w", errMkdir)
	}
	return writeFileAtomic(path, raw, 0o644)
}

// writeFileAtomic writes through a temporary file in the same directory, so a
// concurrent reader sees either the old catalogue or the new one, never a
// half-written one.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	temp, errCreate := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if errCreate != nil {
		return errCreate
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, errWrite := temp.Write(data); errWrite != nil {
		temp.Close()
		return errWrite
	}
	if errChmod := temp.Chmod(mode); errChmod != nil {
		temp.Close()
		return errChmod
	}
	if errClose := temp.Close(); errClose != nil {
		return errClose
	}
	return os.Rename(tempName, path)
}

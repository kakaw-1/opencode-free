package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// minimalCatalogue is enough to satisfy parseCatalogue, which only insists on a
// "read" entry.
const minimalCatalogue = `{"read":{"description":"d","parameters":{"type":"object"}}}`

// withToolsServer points defaultToolsURL at a local server for one test and
// restores it afterwards, so these tests never touch the real upstream.
func withToolsServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	original := defaultToolsURL
	defaultToolsURL = server.URL
	t.Cleanup(func() { defaultToolsURL = original })
}

func TestResolveCataloguePathHonoursToolsFile(t *testing.T) {
	// An explicit path must be used verbatim and must not trigger a download:
	// that is what lets an air-gapped host avoid waiting on a timeout.
	withToolsServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("resolveCataloguePath fetched the catalogue even though tools-file was set")
	})
	path, errResolve := resolveCataloguePath("  /etc/cpa/opencode_tools.json  ", "")
	if errResolve != nil {
		t.Fatalf("resolveCataloguePath: %v", errResolve)
	}
	if path != "/etc/cpa/opencode_tools.json" {
		t.Errorf("path = %q, want the trimmed tools-file value", path)
	}
}

func TestCatalogueCachePathUsesOverride(t *testing.T) {
	path, errCache := catalogueCachePath("/var/cache/cpa/tools.json")
	if errCache != nil {
		t.Fatalf("catalogueCachePath: %v", errCache)
	}
	if path != "/var/cache/cpa/tools.json" {
		t.Errorf("path = %q, want the override", path)
	}
}

func TestCatalogueCachePathDefaultsUnderUserCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path, errCache := catalogueCachePath("")
	if errCache != nil {
		t.Fatalf("catalogueCachePath: %v", errCache)
	}
	if !strings.HasSuffix(path, filepath.Join(pluginID, "opencode_tools.json")) {
		t.Errorf("path = %q, want it under the %s cache directory", path, pluginID)
	}
}

func TestRefreshCatalogueWritesUsableFile(t *testing.T) {
	withToolsServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(minimalCatalogue))
	})
	target := filepath.Join(t.TempDir(), "nested", "opencode_tools.json")
	if errRefresh := refreshCatalogue(target); errRefresh != nil {
		t.Fatalf("refreshCatalogue: %v", errRefresh)
	}
	catalogue, errLoad := loadCatalogue(target)
	if errLoad != nil {
		t.Fatalf("the downloaded catalogue should load: %v", errLoad)
	}
	if _, ok := catalogue[injectedTool]; !ok {
		t.Error("downloaded catalogue is missing the read definition")
	}
}

func TestRefreshCatalogueRejectsNonCatalogue(t *testing.T) {
	// A body that is not a catalogue must not reach the cache, or the next start
	// would load garbage and fail confusingly.
	withToolsServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"bash":{"description":"x"}}`))
	})
	target := filepath.Join(t.TempDir(), "opencode_tools.json")
	if errRefresh := refreshCatalogue(target); errRefresh == nil {
		t.Fatal("a catalogue without a read definition should be rejected")
	}
	if _, errStat := os.Stat(target); errStat == nil {
		t.Error("a rejected download must not be written to the cache")
	}
}

func TestRefreshCatalogueRejectsHTTPError(t *testing.T) {
	withToolsServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	target := filepath.Join(t.TempDir(), "opencode_tools.json")
	if errRefresh := refreshCatalogue(target); errRefresh == nil {
		t.Fatal("a 500 from the source should be reported")
	}
}

func TestResolveCataloguePathFetchesWhenUnset(t *testing.T) {
	withToolsServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(minimalCatalogue))
	})
	cache := filepath.Join(t.TempDir(), "opencode_tools.json")
	path, errResolve := resolveCataloguePath("", cache)
	if errResolve != nil {
		t.Fatalf("resolveCataloguePath: %v", errResolve)
	}
	if path != cache {
		t.Errorf("path = %q, want the cache path %q", path, cache)
	}
	if _, errStat := os.Stat(cache); errStat != nil {
		t.Errorf("the catalogue should have been cached: %v", errStat)
	}
}

func TestResolveCataloguePathReusesAFreshCache(t *testing.T) {
	// The host calls plugin.reconfigure several times during one startup, so a
	// copy younger than the refresh interval must be reused rather than
	// re-downloaded once per call.
	withToolsServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a fresh cache should not be re-fetched")
	})
	cache := filepath.Join(t.TempDir(), "opencode_tools.json")
	if errWrite := os.WriteFile(cache, []byte(minimalCatalogue), 0o644); errWrite != nil {
		t.Fatalf("seed cache: %v", errWrite)
	}
	path, errResolve := resolveCataloguePath("", cache)
	if errResolve != nil {
		t.Fatalf("resolveCataloguePath: %v", errResolve)
	}
	if path != cache {
		t.Errorf("path = %q, want the cache path", path)
	}
}

func TestResolveCataloguePathRefreshesAStaleCache(t *testing.T) {
	fetched := false
	withToolsServer(t, func(w http.ResponseWriter, r *http.Request) {
		fetched = true
		_, _ = w.Write([]byte(minimalCatalogue))
	})
	cache := filepath.Join(t.TempDir(), "opencode_tools.json")
	if errWrite := os.WriteFile(cache, []byte(`{"stale":true}`), 0o644); errWrite != nil {
		t.Fatalf("seed cache: %v", errWrite)
	}
	aged := time.Now().Add(-2 * catalogueRefreshInterval)
	if errChtimes := os.Chtimes(cache, aged, aged); errChtimes != nil {
		t.Fatalf("age the cache: %v", errChtimes)
	}
	if _, errResolve := resolveCataloguePath("", cache); errResolve != nil {
		t.Fatalf("resolveCataloguePath: %v", errResolve)
	}
	if !fetched {
		t.Error("a cache older than the refresh interval should be re-fetched")
	}
	raw, errRead := os.ReadFile(cache)
	if errRead != nil {
		t.Fatalf("read cache: %v", errRead)
	}
	if string(raw) == `{"stale":true}` {
		t.Error("the stale cache should have been replaced")
	}
}

func TestResolveCataloguePathFallsBackToCache(t *testing.T) {
	// A host that fetched once must keep working when the network is gone: that
	// is the whole point of caching rather than refusing to start.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	original := defaultToolsURL
	defaultToolsURL = dead.URL
	t.Cleanup(func() { defaultToolsURL = original })

	cache := filepath.Join(t.TempDir(), "opencode_tools.json")
	if errWrite := os.WriteFile(cache, []byte(minimalCatalogue), 0o644); errWrite != nil {
		t.Fatalf("seed cache: %v", errWrite)
	}
	// Age it, or the fresh copy would be reused and the refresh never attempted.
	aged := time.Now().Add(-2 * catalogueRefreshInterval)
	if errChtimes := os.Chtimes(cache, aged, aged); errChtimes != nil {
		t.Fatalf("age the cache: %v", errChtimes)
	}
	path, errResolve := resolveCataloguePath("", cache)
	if errResolve != nil {
		t.Fatalf("a cached copy should be used when the refresh fails: %v", errResolve)
	}
	if path != cache {
		t.Errorf("path = %q, want the cache path", path)
	}
}

func TestResolveCataloguePathFailsWithoutCacheOrNetwork(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	original := defaultToolsURL
	defaultToolsURL = dead.URL
	t.Cleanup(func() { defaultToolsURL = original })

	_, errResolve := resolveCataloguePath("", filepath.Join(t.TempDir(), "opencode_tools.json"))
	if errResolve == nil {
		t.Fatal("no catalogue and no network should be an error, not a silent no-op")
	}
	if !strings.Contains(errResolve.Error(), "fetch-tools.sh") {
		t.Errorf("the error should name the fix, got: %v", errResolve)
	}
}

func TestWriteFileAtomicReplacesContent(t *testing.T) {
	target := filepath.Join(t.TempDir(), "file.json")
	if errWrite := writeFileAtomic(target, []byte("first"), 0o644); errWrite != nil {
		t.Fatalf("writeFileAtomic: %v", errWrite)
	}
	if errWrite := writeFileAtomic(target, []byte("second"), 0o644); errWrite != nil {
		t.Fatalf("writeFileAtomic overwrite: %v", errWrite)
	}
	raw, errRead := os.ReadFile(target)
	if errRead != nil {
		t.Fatalf("read back: %v", errRead)
	}
	if string(raw) != "second" {
		t.Errorf("content = %q, want %q", raw, "second")
	}
	// The temporary file must not be left behind next to the target.
	entries, errReadDir := os.ReadDir(filepath.Dir(target))
	if errReadDir != nil {
		t.Fatalf("read dir: %v", errReadDir)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("leftover files after an atomic write: %v", names)
	}
}

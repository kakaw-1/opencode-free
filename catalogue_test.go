package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCatalogueRequiresPath(t *testing.T) {
	// There is no embedded fallback by design: the definitions are OpenCode's
	// data and this plugin is MIT, so they are supplied at runtime. An empty
	// path must fail loudly rather than registering a plugin that cannot work.
	_, errLoad := loadCatalogue("")
	if errLoad == nil {
		t.Fatal("an empty tools-file should be an error, not a silent fallback")
	}
	if !strings.Contains(errLoad.Error(), "fetch-tools.sh") {
		t.Errorf("the error should name the fix, got: %v", errLoad)
	}
}

func TestLoadCatalogueMissingFile(t *testing.T) {
	_, errLoad := loadCatalogue(filepath.Join(t.TempDir(), "nope.json"))
	if errLoad == nil {
		t.Fatal("a missing catalogue file should be reported")
	}
}

func TestParseCatalogueRejectsCataloguesWithoutRead(t *testing.T) {
	// Without "read" every rewritten request would still 403, so a catalogue
	// missing it is a configuration error worth failing registration over.
	_, errParse := parseCatalogue([]byte(`{"bash":{"description":"x","parameters":{}}}`), "(test)")
	if errParse == nil {
		t.Fatal("a catalogue without a read definition should be rejected")
	}
}

func TestParseCatalogueRejectsNonJSON(t *testing.T) {
	if _, errParse := parseCatalogue([]byte("not json"), "(test)"); errParse == nil {
		t.Fatal("non-JSON should be rejected")
	}
}

func TestParseCatalogueAcceptsMinimalRead(t *testing.T) {
	defs, errParse := parseCatalogue([]byte(`{"read":{"description":"d","parameters":{"type":"object"}}}`), "(test)")
	if errParse != nil {
		t.Fatalf("a minimal catalogue with read should parse: %v", errParse)
	}
	if _, ok := defs[injectedTool]; !ok {
		t.Fatalf("read missing from the parsed catalogue: %v", defs)
	}
}

// TestFetchedCatalogueIsUsable runs only when fetch-tools.sh has been run. It is
// the check that the file the script writes is the shape the injector expects.
func TestFetchedCatalogueIsUsable(t *testing.T) {
	if _, errStat := os.Stat("opencode_tools.json"); errStat != nil {
		t.Skip("opencode_tools.json not present; run ./fetch-tools.sh")
	}
	catalogue, errLoad := loadCatalogue("opencode_tools.json")
	if errLoad != nil {
		t.Fatalf("load fetched catalogue: %v", errLoad)
	}
	read, ok := catalogue[injectedTool]
	if !ok {
		t.Fatal("fetched catalogue has no read definition")
	}
	if strings.TrimSpace(read.Description) == "" {
		t.Error("read definition has an empty description; the model gets this text, so it has to say what the tool does")
	}
	if len(read.Parameters) == 0 {
		t.Error("read definition has no parameters; the model gets this schema, so it cannot be empty")
	}
}

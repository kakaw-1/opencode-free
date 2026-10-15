package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestConfigPathFromArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"space form", []string{"cpa", "-config", "/etc/cpa/config.yaml"}, "/etc/cpa/config.yaml"},
		{"double dash space", []string{"cpa", "--config", "/etc/cpa/config.yaml"}, "/etc/cpa/config.yaml"},
		{"equals form", []string{"cpa", "-config=/etc/cpa/config.yaml"}, "/etc/cpa/config.yaml"},
		{"double dash equals", []string{"cpa", "--config=/etc/cpa/config.yaml"}, "/etc/cpa/config.yaml"},
		{"absent", []string{"cpa", "-tui"}, ""},
		{"dangling flag", []string{"cpa", "-config"}, ""},
		// CPA stops scanning at "--", so anything after it is positional.
		{"after terminator", []string{"cpa", "--", "-config", "/etc/cpa/config.yaml"}, ""},
	}
	for _, tc := range cases {
		if got := configPathFromArgs(tc.args); got != tc.want {
			t.Errorf("%s: configPathFromArgs(%q) = %q, want %q", tc.name, tc.args, got, tc.want)
		}
	}
}

func TestDiscoverProvidersFromConfigFile(t *testing.T) {
	const doc = `
config-version: 8
api-keys:
  openai-compatibility:
    - name: "opencode"
      base-url: "https://opencode.ai/inference/v1"
      models:
        - name: "mimo-v2.6-flash-free"
    - name: "openrouter"
      base-url: "https://openrouter.ai/api/v1"
    - name: "retired"
      disabled: true
      base-url: "https://example.invalid/v1"
  claude-api-key:
    - api-key: "sk-ant"
`
	got := mustDiscover(t, doc)
	want := []string{"opencode", "openrouter"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("discoverProvidersFromConfigFile() = %q, want %q", got, want)
	}
}

func TestDiscoverProvidersHandlesV8Layout(t *testing.T) {
	// The v8 layout moved the credential families under `access`. Discovery
	// searches the tree rather than a fixed path so both spellings work.
	const doc = `
config-version: 8
access:
  api-keys:
    openai-compatibility:
      - name: "opencode"
        base-url: "https://opencode.ai/inference/v1"
`
	got := mustDiscover(t, doc)
	if want := []string{"opencode"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("discoverProvidersFromConfigFile() = %q, want %q", got, want)
	}
}

func TestDiscoverProvidersDefaultsUnnamedGroup(t *testing.T) {
	// The synthesizer falls back to "openai-compatibility" for a group with no
	// name, so discovery must report the same name or validation would reject a
	// provider the host really serves.
	const doc = `
api-keys:
  openai-compatibility:
    - base-url: "https://example.test/v1"
`
	got := mustDiscover(t, doc)
	if want := []string{unnamedProviderName}; !reflect.DeepEqual(got, want) {
		t.Fatalf("discoverProvidersFromConfigFile() = %q, want %q", got, want)
	}
}

func TestDiscoverProvidersWithoutOpenAICompatGroups(t *testing.T) {
	const doc = `
access:
  api-keys:
    - "client-key"
api-keys:
  claude-api-key:
    - api-key: "sk-ant"
`
	got := mustDiscover(t, doc)
	if len(got) != 0 {
		t.Fatalf("discoverProvidersFromConfigFile() = %q, want none", got)
	}
}

func TestDiscoverProvidersFromConfigFileMissing(t *testing.T) {
	if _, errDiscover := discoverProvidersFromConfigFile(filepath.Join(t.TempDir(), "nope.yaml")); errDiscover == nil {
		t.Fatal("a missing config file should be reported, not silently treated as empty")
	}
}

func TestUnknownProviders(t *testing.T) {
	cases := []struct {
		name       string
		selected   []string
		discovered []string
		want       []string
	}{
		{"all known", []string{"opencode"}, []string{"opencode", "openrouter"}, nil},
		{"case insensitive", []string{"OpenCode"}, []string{"opencode"}, nil},
		{"one unknown", []string{"opencode", "typo"}, []string{"opencode"}, []string{"typo"}},
		{"nothing discovered", []string{"opencode"}, nil, []string{"opencode"}},
		// The wildcard is not a group name, so a typo check must not report it.
		{"wildcard is never unknown", []string{"*"}, []string{"opencode"}, nil},
		{"wildcard among names", []string{"*", "typo"}, []string{"opencode"}, []string{"typo"}},
		{"wildcard without discovery", []string{"*"}, nil, nil},
	}
	for _, tc := range cases {
		got := unknownProviders(tc.selected, tc.discovered)
		if len(got) != len(tc.want) {
			t.Errorf("%s: unknownProviders(%q, %q) = %q, want %q", tc.name, tc.selected, tc.discovered, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: unknownProviders(%q, %q) = %q, want %q", tc.name, tc.selected, tc.discovered, got, tc.want)
			}
		}
	}
}

func TestStringListAcceptsScalarAndSequence(t *testing.T) {
	// The point of the custom unmarshaler: one selection need not be written as
	// a one-element list.
	cases := []struct {
		name string
		yaml string
		want []string
	}{
		{"scalar", "providers: opencode\n", []string{"opencode"}},
		{"sequence", "providers:\n  - opencode\n  - openrouter\n", []string{"opencode", "openrouter"}},
		{"absent", "models: [x]\n", nil},
	}
	for _, tc := range cases {
		var cfg pluginConfig
		if errUnmarshal := yaml.Unmarshal([]byte(tc.yaml), &cfg); errUnmarshal != nil {
			t.Errorf("%s: %v", tc.name, errUnmarshal)
			continue
		}
		if !reflect.DeepEqual([]string(cfg.Providers), tc.want) {
			t.Errorf("%s: Providers = %q, want %q", tc.name, cfg.Providers, tc.want)
		}
	}
}

// mustDiscover writes doc to a temp config file and discovers from it.
func mustDiscover(t *testing.T, doc string) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte(doc), 0o600); errWrite != nil {
		t.Fatalf("write temp config: %v", errWrite)
	}
	names, errDiscover := discoverProvidersFromConfigFile(path)
	if errDiscover != nil {
		t.Fatalf("discoverProvidersFromConfigFile: %v", errDiscover)
	}
	return names
}

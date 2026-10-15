package main

// Discovering the OpenAI-compatible providers from CPA's own config file.
//
// host.auth.list would be the natural source, but it does not report
// api-keys-derived credentials. buildHostAuthFileEntry drops every entry whose
// attributes carry neither "path" nor "runtime_only", and a config-synthesized
// auth has neither (synthesizer/config.go sets source, base_url, compat_name,
// provider_key, config_index). Verified against a live host: a config with one
// openai-compatibility group yields {"files":[]}.
//
// So read the config file instead. Its path comes from this process's own argv,
// the same way CPA resolves it for plugin bootstrap — see
// pluginBootstrapConfigPath in cmd/server/main.go, which this mirrors.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// defaultConfigFileName matches cmd/server's fallback when -config is absent.
const defaultConfigFileName = "config.yaml"

// openAICompatConfigKey is the config key holding OpenAI-compatible groups.
const openAICompatConfigKey = "openai-compatibility"

// unnamedProviderName mirrors the synthesizer's default for a group without a
// name, so validation does not reject a name the host actually uses.
const unnamedProviderName = "openai-compatibility"

// configProvider is the subset of one openai-compatibility group this plugin
// needs. Nothing else in the file is decoded — the rest holds credentials.
type configProvider struct {
	Name     string `yaml:"name"`
	Disabled bool   `yaml:"disabled"`
}

// resolveHostConfigPath finds the config file the host was started with.
//
// It reads /proc/self/cmdline, which is this process's own argv: the plugin is a
// shared object inside the host, so "self" is the host.
func resolveHostConfigPath() (string, error) {
	if raw, errRead := os.ReadFile("/proc/self/cmdline"); errRead == nil {
		if path := configPathFromArgs(splitNulList(raw)); path != "" {
			return path, nil
		}
	}
	wd, errGetwd := os.Getwd()
	if errGetwd != nil {
		return "", fmt.Errorf("no -config in argv and getwd failed: %w", errGetwd)
	}
	return filepath.Join(wd, defaultConfigFileName), nil
}

// splitNulList splits a NUL-separated argv blob, dropping the trailing empty
// element.
func splitNulList(raw []byte) []string {
	parts := strings.Split(string(raw), "\x00")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// configPathFromArgs extracts the -config value, accepting the space and "="
// forms and both one- and two-dash spellings. Returns "" when absent.
func configPathFromArgs(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return ""
		case arg == "-config" || arg == "--config":
			if i+1 < len(args) {
				return strings.TrimSpace(args[i+1])
			}
			return ""
		case strings.HasPrefix(arg, "-config="):
			return strings.TrimSpace(strings.TrimPrefix(arg, "-config="))
		case strings.HasPrefix(arg, "--config="):
			return strings.TrimSpace(strings.TrimPrefix(arg, "--config="))
		}
	}
	return ""
}

// discoverProvidersFromConfigFile returns the OpenAI-compatible group names
// declared in the host's config, skipping disabled groups.
func discoverProvidersFromConfigFile(path string) ([]string, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return nil, fmt.Errorf("read host config %s: %w", path, errRead)
	}
	var doc yaml.Node
	if errUnmarshal := yaml.Unmarshal(raw, &doc); errUnmarshal != nil {
		return nil, fmt.Errorf("parse host config %s: %w", path, errUnmarshal)
	}
	return providerNames(findOpenAICompatGroups(&doc)), nil
}

// findOpenAICompatGroups collects every openai-compatibility group in the
// document. A recursive search rather than a fixed path, because the key sits at
// `api-keys.openai-compatibility` in the legacy layout and under `access` in the
// v8 one, and a partially migrated document can hold both.
func findOpenAICompatGroups(node *yaml.Node) []configProvider {
	var found []configProvider
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil {
			return
		}
		if n.Kind != yaml.MappingNode {
			for _, child := range n.Content {
				walk(child)
			}
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if key.Value == openAICompatConfigKey && value.Kind == yaml.SequenceNode {
				var groups []configProvider
				if errDecode := value.Decode(&groups); errDecode == nil {
					found = append(found, groups...)
					continue
				}
			}
			walk(value)
		}
	}
	walk(node)
	return found
}

// providerNames reduces groups to the sorted, de-duplicated names the host will
// actually serve.
func providerNames(groups []configProvider) []string {
	seen := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		if group.Disabled {
			continue
		}
		name := strings.TrimSpace(group.Name)
		if name == "" {
			name = unnamedProviderName
		}
		seen[name] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

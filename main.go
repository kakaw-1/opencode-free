package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

// Kept so host callbacks (host.auth.list) can be issued from inside a plugin
// method. The host owns the buffer it hands back, so it must be released with
// the host's own free, not ours.
static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

// pluginID must match the key under plugins.configs.<id> and the library file
// name (opencode-free.so).
const pluginID = "opencode-free"

// The host rejects registration unless Name, Version, Author, and
// GitHubRepository are all non-empty (see pluginhost.validPlugin), so these are
// load-bearing, not decoration. Only the emptiness is checked, but they are
// shown to operators, so point them at the real repo.
const pluginVersion = "0.1.1"

// Vars rather than consts so `make REPO_URL=... AUTHOR=...` can stamp them in at
// build time via -ldflags -X. Publishing this source means setting both — see
// README_CN.md, "发布到 GitHub".
var (
	pluginAuthor  = "local"
	pluginRepoURL = "https://github.com/CHANGE-ME/opencode-free"
)

func main() {}

// pluginConfig is the YAML subtree under plugins.configs.opencode-free, handed
// to us verbatim at registration.
type pluginConfig struct {
	// Providers selects which OpenAI-compatible providers to rewrite, by the
	// group name given under api-keys.openai-compatibility.
	//
	// Accepts a single name or a list, so both of these work:
	//
	//	providers: opencode
	//	providers: [opencode, openrouter]
	//	providers: "*"          # every OpenAI-compatible group
	//
	// The names are discovered from the host's config file at registration and
	// offered back as enum values, so a management UI can render a picker.
	// Anything not selected passes through untouched: this plugin runs for every
	// provider CPA serves.
	//
	// Empty is allowed and means "rewrite nothing". That is the state a
	// plugin-store install starts in, since the store cannot write config.
	Providers stringList `yaml:"providers"`
	// Models optionally narrows further, matched against both the selected
	// upstream model and the client-requested one. "*" is a wildcard. Empty
	// means "every model on the selected providers".
	Models []string `yaml:"models"`
	// ToolsFile names the JSON tool catalogue to inject. The definitions are
	// OpenCode's and are not redistributed with this MIT plugin, so this points
	// at a copy on the operator's own disk; fetch-tools.sh writes one.
	//
	// Optional. When empty the plugin downloads and caches the catalogue itself
	// (see fetch.go), which is what makes a plugin-store install work without a
	// manual second step. Setting it skips the network entirely.
	ToolsFile string `yaml:"tools-file"`
	// ToolsCache overrides where an automatically fetched catalogue is kept.
	// Only consulted when tools-file is empty. Defaults to the OS cache
	// directory, e.g. ~/.cache/opencode-free/opencode_tools.json.
	ToolsCache string `yaml:"tools-cache"`
	// ForceStream sets "stream": true on every rewritten body. The free tier
	// rejects non-streaming requests outright, so turning this off only makes
	// sense for debugging against a different upstream.
	ForceStream *bool `yaml:"force-stream"`
	// DebugLog appends one JSON line per intercepted request, recording what the
	// filter decided. For diagnosing "the plugin isn't doing anything".
	DebugLog string `yaml:"debug-log"`
}

// stringList accepts either a scalar or a sequence in YAML, so a single
// selection does not have to be written as a one-element list.
type stringList []string

// UnmarshalYAML implements yaml.Unmarshaler.
func (s *stringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var value string
		if errDecode := node.Decode(&value); errDecode != nil {
			return errDecode
		}
		*s = stringList{value}
		return nil
	case yaml.SequenceNode:
		var values []string
		if errDecode := node.Decode(&values); errDecode != nil {
			return errDecode
		}
		*s = stringList(values)
		return nil
	default:
		return fmt.Errorf("expected a provider name or a list of provider names")
	}
}

type pluginState struct {
	mu        sync.RWMutex
	config    pluginConfig
	catalogue toolCatalogue
}

var state = pluginState{}

// forceStream reports the effective setting. Unset means true: the gate
// requires streaming, so the default has to be the working value.
func (s *pluginState) forceStream() bool {
	if s.config.ForceStream == nil {
		return true
	}
	return *s.config.ForceStream
}

// registerRequest is the plugin.register / plugin.reconfigure payload.
type registerRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

// registrationCapability declares only the seam this plugin needs. It rewrites
// the outbound request body; it does not own execution, translation, or the
// response path.
type registrationCapability struct {
	RequestInterceptor bool `json:"request_interceptor"`
}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	if host != nil {
		C.store_host_api(host)
	}
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

// callHost issues a host callback and unwraps the {"ok":true,"result":...}
// envelope. Only usable from inside a plugin method: the host resolves the
// calling plugin from the callback context.
func callHost(method string, payload any) (json.RawMessage, error) {
	rawPayload, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal host callback payload %s: %w", method, errMarshal)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		cPayload := C.CBytes(rawPayload)
		if cPayload == nil {
			return nil, fmt.Errorf("allocate host callback payload %s", method)
		}
		defer C.free(cPayload)
		requestPtr = (*C.uint8_t)(cPayload)
	}
	callCode := C.call_host_api(cMethod, requestPtr, C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		// The host allocated this buffer, so it must be released by the host.
		C.free_host_buffer(response.ptr, response.len)
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("host callback %s returned no response, code=%d", method, int(callCode))
	}

	var env pluginabi.Envelope
	if errUnmarshal := json.Unmarshal(rawResponse, &env); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host callback envelope %s: %w", method, errUnmarshal)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("host callback %s failed: %s", method, env.Error.Message)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	return env.Result, nil
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required", 0))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error(), 0))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = len
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.catalogue = nil
	state.config = pluginConfig{}
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errConfigure := configure(request); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodRequestInterceptAfter:
		return interceptAfterAuth(request)
	case pluginabi.MethodRequestInterceptBefore:
		// The body cannot be rewritten before credential selection: ToFormat is
		// empty there, so there is no way to know the upstream speaks Chat
		// Completions. Leave the earlier hook alone.
		return passThrough(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method, 0), nil
	}
}

func configure(raw []byte) error {
	var req registerRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return errUnmarshal
		}
	}
	cfg := pluginConfig{}
	if len(req.ConfigYAML) > 0 {
		if errUnmarshal := yaml.Unmarshal(req.ConfigYAML, &cfg); errUnmarshal != nil {
			return fmt.Errorf("parse %s config: %w", pluginID, errUnmarshal)
		}
	}
	cfg.ToolsFile = strings.TrimSpace(cfg.ToolsFile)
	cfg.ToolsCache = strings.TrimSpace(cfg.ToolsCache)
	cfg.DebugLog = strings.TrimSpace(cfg.DebugLog)
	cfg.Providers = stringList(trimmedPatterns(cfg.Providers))
	cfg.Models = trimmedPatterns(cfg.Models)
	// An empty selection registers inert instead of failing registration.
	//
	// Failing loudly was the first design, and it is right for a hand-edited
	// config: a plugin that loads and does nothing looks like "installed but
	// broken", which is worse than one that refuses to start. The plugin store
	// inverts that trade. It installs the library and enables the plugin but
	// cannot write its config, so refusing to register there produces exactly
	// the silent no-op the loud error existed to prevent: a 200 response
	// saying restart_required: false, and a plugin that is not loaded.
	//
	// Inert is still safe. With no selection nothing is rewritten, which is the
	// property that actually matters; the failure mode is "does nothing and
	// says so", not "rewrites the wrong traffic".
	if len(cfg.Providers) == 0 {
		logHost("warn", fmt.Sprintf("%s: no `providers` selection, so no request will be rewritten. Set it to the OpenAI-compatible groups from `api-keys.openai-compatibility` (e.g. `providers: opencode`), or to `*` for every such group. Discovered: %s", pluginID, describeDiscoveredProviders()))
	}
	// A bad catalogue fails registration loudly. Loading inert would look like
	// "the plugin is installed but does nothing", which is much harder to
	// diagnose than a plugin that refuses to start. resolveCataloguePath is what
	// makes an unset `tools-file` work: it downloads and caches the definitions
	// rather than giving up.
	cataloguePath, errResolve := resolveCataloguePath(cfg.ToolsFile, cfg.ToolsCache)
	if errResolve != nil {
		return errResolve
	}
	catalogue, errLoad := loadCatalogue(cataloguePath)
	if errLoad != nil {
		return errLoad
	}
	// Say where the definitions came from. When the plugin fetched them itself
	// this is the only way to find the cache, and when the gate changes it is
	// the first thing worth checking.
	logHost("info", fmt.Sprintf("%s: loaded %d tool definitions from %s", pluginID, len(catalogue), cataloguePath))
	// Discovery is advisory. When the host cannot enumerate the configured
	// OpenAI-compatible groups, an explicitly named selection is still honoured:
	// the name is the one the operator wrote in their own config, so there is
	// nothing to validate against.
	discovered, errDiscover := listOpenAICompatProviders()
	switch {
	case errDiscover != nil:
		logHost("warn", fmt.Sprintf("%s: could not read the host config to discover providers, accepting `providers` as given: %v", pluginID, errDiscover))
	case len(discovered) == 0:
		logHost("warn", fmt.Sprintf("%s: the host config declares no openai-compatibility groups, accepting `providers` as given: %s", pluginID, strings.Join(cfg.Providers, ", ")))
	default:
		if unknown := unknownProviders(cfg.Providers, discovered); len(unknown) > 0 {
			return fmt.Errorf("%s: unknown provider(s) %s; the host serves: %s", pluginID, strings.Join(unknown, ", "), strings.Join(discovered, ", "))
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.config = cfg
	state.catalogue = catalogue
	return nil
}

// listOpenAICompatProviders reads the OpenAI-compatible group names out of the
// host's config file. See configfile.go for why the host's own credential
// callback cannot answer this.
func listOpenAICompatProviders() ([]string, error) {
	path, errResolve := resolveHostConfigPath()
	if errResolve != nil {
		return nil, errResolve
	}
	return discoverProvidersFromConfigFile(path)
}

// unknownProviders returns the selected names discovery did not see. The
// wildcard is not a name, so it is never reported as unknown.
func unknownProviders(selected, discovered []string) []string {
	known := make(map[string]struct{}, len(discovered))
	for _, name := range discovered {
		known[strings.ToLower(name)] = struct{}{}
	}
	var unknown []string
	for _, name := range selected {
		if name == wildcardProvider {
			continue
		}
		if _, ok := known[strings.ToLower(name)]; !ok {
			unknown = append(unknown, name)
		}
	}
	return unknown
}

// describeDiscoveredProviders renders the discovery result for an error message.
func describeDiscoveredProviders() string {
	discovered, errDiscover := listOpenAICompatProviders()
	if errDiscover != nil {
		return fmt.Sprintf("(discovery unavailable: %v)", errDiscover)
	}
	if len(discovered) == 0 {
		return "(none reported by the host)"
	}
	return strings.Join(discovered, ", ")
}

// logHost writes to the host log. Diagnostics must never break a request, so
// failures are swallowed.
func logHost(level, message string) {
	_, _ = callHost(pluginabi.MethodHostLog, map[string]any{"level": level, "message": message})
}

// trimmedPatterns drops blank entries so a stray "-" in the YAML does not turn
// into a pattern that matches nothing.
func trimmedPatterns(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginID,
			Version:          pluginVersion,
			Author:           pluginAuthor,
			GitHubRepository: pluginRepoURL,
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "providers",
					Type:        pluginapi.ConfigFieldTypeArray,
					Description: "OpenAI-compatible providers to rewrite, named as in `api-keys.openai-compatibility[].name`. Accepts one name or a list; `*` selects every OpenAI-compatible group. Anything not selected passes through untouched. Leave it empty and the plugin registers but rewrites nothing — a plugin-store install lands in exactly that state, because the store installs and enables a plugin but cannot write its config.",
					EnumValues:  providerEnumValues(),
				},
				{
					Name:        "models",
					Type:        pluginapi.ConfigFieldTypeArray,
					Description: "Optional narrowing on top of `providers`, matched against the upstream model and the client-requested model; '*' is a wildcard. Empty means every model on the selected providers.",
				},
				{
					Name:        "tools-file",
					Type:        pluginapi.ConfigFieldTypeString,
					Description: "Optional. Path to a local JSON tool catalogue (the official OpenCode definitions). Not bundled: they are GPL-3.0-or-later data and this plugin is MIT, so they are supplied at runtime. Leave empty and the plugin downloads and caches them itself; set it to skip the network, e.g. on an air-gapped host. Run fetch-tools.sh to write a copy.",
				},
				{
					Name:        "tools-cache",
					Type:        pluginapi.ConfigFieldTypeString,
					Description: "Optional. Where an automatically downloaded tool catalogue is cached. Only used when `tools-file` is empty. Defaults to the OS cache directory, e.g. ~/.cache/opencode-free/opencode_tools.json.",
				},
				{
					Name:        "force-stream",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Description: "Set stream:true on rewritten requests (default true). OpenCode's free tier rejects non-streaming requests.",
				},
				{
					Name:        "debug-log",
					Type:        pluginapi.ConfigFieldTypeString,
					Description: "Optional file path. Appends one JSON line per intercepted request recording the filter decision.",
				},
			},
		},
		Capabilities: registrationCapability{RequestInterceptor: true},
	}
}

// discoveredProviderEnum offers the host's OpenAI-compatible group names so a
// management UI can render a picker. Nil when discovery is unavailable, which
// leaves the field free-form.
func discoveredProviderEnum() []string {
	discovered, errDiscover := listOpenAICompatProviders()
	if errDiscover != nil || len(discovered) == 0 {
		return nil
	}
	return discovered
}

// providerEnumValues is what the management UI offers for `providers`: the
// host's OpenAI-compatible group names plus the wildcard. Enum values on an
// array field are advisory (only ConfigFieldTypeEnum constrains a value), so a
// name discovery missed can still be written by hand.
//
// The wildcard is only offered once discovery worked. If it did not, the field
// stays free-form rather than advertising "*" as the single choice.
func providerEnumValues() []string {
	discovered := discoveredProviderEnum()
	if len(discovered) == 0 {
		return nil
	}
	return append([]string{wildcardProvider}, discovered...)
}

// interceptAfterAuth rewrites the outbound Chat Completions body so OpenCode's
// free tier accepts it. This is the whole plugin.
//
// It runs for every provider CPA serves, so it does nothing unless the model
// matches the configured filter. An empty `providers` means the filter matches
// nothing, which is how an unconfigured plugin stays inert.
func interceptAfterAuth(raw []byte) ([]byte, error) {
	var req pluginapi.RequestInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}

	state.mu.RLock()
	catalogue := state.catalogue
	forceStream := state.forceStream()
	debugLog := state.config.DebugLog
	providers := state.config.Providers
	models := state.config.Models
	state.mu.RUnlock()

	decision := "skip"
	authID := selectedAuthID(req.Metadata)
	switch {
	case catalogue == nil:
		decision = "skip:not-registered"
	case len(providers) == 0:
		// Registered inert: the store installed the plugin but its config has
		// no selection yet. Reported separately from skip:provider so a debug
		// log says "you have not chosen anything" rather than "you chose
		// something else".
		decision = "skip:no-selection"
	case !providerSelectionMatches(providers, authID):
		decision = "skip:provider"
	case len(models) > 0 && !modelMatches(models, req.Model, req.RequestedModel):
		decision = "skip:model"
	default:
		decision = "rewrite"
	}

	if decision != "rewrite" {
		debugIntercept(debugLog, req, authID, decision, false)
		return okEnvelope(pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body})
	}

	body, changed := prepareBody(req.Body, forceStream, catalogue)
	debugIntercept(debugLog, req, authID, decision, changed)
	if !changed {
		return okEnvelope(pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body})
	}
	return okEnvelope(pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: body})
}

// debugIntercept appends one JSON line per intercepted request when debug-log
// is configured. Failures are swallowed: diagnostics must never break a request.
func debugIntercept(path string, req pluginapi.RequestInterceptRequest, authID, decision string, changed bool) {
	if path == "" {
		return
	}
	line, errMarshal := json.Marshal(map[string]any{
		"decision":         decision,
		"changed":          changed,
		"selected_auth_id": authID,
		"model":            req.Model,
		"requested_model":  req.RequestedModel,
		"source_format":    req.SourceFormat,
		"to_format":        req.ToFormat,
		"stream":           req.Stream,
		"body_bytes":       len(req.Body),
	})
	if errMarshal != nil {
		return
	}
	file, errOpen := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if errOpen != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(line, '\n'))
}

func passThrough(raw []byte) ([]byte, error) {
	var req pluginapi.RequestInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	return okEnvelope(pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body})
}

func okEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(pluginabi.Envelope{OK: true, Result: raw})
}

// errorEnvelope carries an HTTP status so CPA maps the failure to a structured
// client error instead of degrading everything to a 500.
func errorEnvelope(code, message string, httpStatus int) []byte {
	raw, errMarshal := pluginabi.NewErrorEnvelope(code, message, httpStatus)
	if errMarshal != nil {
		return []byte(`{"ok":false,"error":{"code":"plugin_error","message":"encode error"}}`)
	}
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

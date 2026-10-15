# Builds the CPA plugin. CPA loads C ABI shared libraries, so the package needs
# cgo and -buildmode=c-shared.
#
#   make          -> opencode-free.so
#   make test     -> run the body-rewriting tests
#   make vet      -> go vet
#   make install  -> copy the .so into CPA's plugin directory
#   make dist     -> tarball with sources + build script, for other machines
#
# CPA discovers plugins in <plugins.dir>/<goos>/<goarch>/ and then in
# <plugins.dir>/ (default plugins.dir is "plugins"), matching <id>.so.
#
# Build on the machine that will run it. The .so links against the host libc, so
# one built on a new glibc will not load on an older one; use `make dist` and
# build on the target rather than copying the .so around.

PLUGIN := opencode-free
LIB    := $(PLUGIN).so

# opencode_tools.json is deliberately absent: it is fetched at runtime, not
# embedded, and not redistributed here. See catalogue.go and .gitignore.
SOURCES := $(wildcard *.go) go.mod go.sum

# Files that make up a source release.
DIST_FILES := $(SOURCES) $(wildcard *.sh) Makefile README.md README_CN.md \
	LICENSE NOTICE config.example.yaml registry.json .gitignore

# Override GO to use a specific toolchain, e.g. make GO=/usr/local/go/bin/go
GO ?= go

# Stamped into the binary via -ldflags. CPA shows these to operators and rejects
# registration if either is empty. Override for a fork:
#
#   make REPO_URL=https://github.com/you/opencode-free AUTHOR=you
REPO_URL ?= https://github.com/kakaw-1/opencode-free
AUTHOR   ?= kakaw-1
LDFLAGS  := -X main.pluginRepoURL=$(REPO_URL) -X main.pluginAuthor=$(AUTHOR)

# Where CPA looks for plugin libraries. Override to match your deployment.
PLUGIN_DIR ?= plugins

.PHONY: all test vet install dist clean

all: $(LIB)

$(LIB): $(SOURCES)
	CGO_ENABLED=1 $(GO) build -buildmode=c-shared -ldflags '$(LDFLAGS)' -o $@ .

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

install: $(LIB)
	install -d $(PLUGIN_DIR)
	install -m 0644 $(LIB) $(PLUGIN_DIR)/$(LIB)

# Sources only — no prebuilt .so, deliberately. The tarball is meant to be
# unpacked and built on the target machine.
dist:
	rm -rf dist $(PLUGIN).tar.gz
	install -d dist/$(PLUGIN)
	install -m 0644 $(DIST_FILES) dist/$(PLUGIN)/
	chmod 0755 dist/$(PLUGIN)/*.sh
	tar -C dist -czf $(PLUGIN).tar.gz $(PLUGIN)
	rm -rf dist
	@echo "wrote $(PLUGIN).tar.gz"

clean:
	rm -f $(LIB) $(PLUGIN).h
	rm -rf dist $(PLUGIN).tar.gz

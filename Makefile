BINARY := agent-bridge-mcp
# CMD is the MCP server's main package (module: github.com/adubkov/agent-bridge).
CMD    := ./cmd/$(BINARY)

MARKETPLACE := agent-bridge-local
PLUGIN      := agent-bridge

# Where the Antigravity CLI copies imported plugins. agy imports the plugin
# MANIFESTS but not the built binary and has no ${CLAUDE_PLUGIN_ROOT} support, so
# install-agy copies a FROZEN binary into this dir and repoints the imported
# mcp_config.json at it. Override AGY_PLUGIN_DIR if your agy layout differs.
AGY_PLUGIN_DIR := $(HOME)/.gemini/config/plugins/$(PLUGIN)

.PHONY: build install vet test clean smoke smoke-gemini smoke-claude smoke-codex install-claude uninstall-claude install-agy uninstall-agy install-codex uninstall-codex install-all uninstall-all help

## build: compile the MCP server (cmd/agent-bridge-mcp) into the REPO ROOT
##        (./agent-bridge-mcp). The install-* targets copy this freshly built binary
##        into each host's own plugin install — a frozen, per-host snapshot, so editing
##        or rebuilding this checkout never changes an already-installed agent.
build:
	go build -o $(BINARY) $(CMD)

## install: OPTIONAL — `go install` the MCP server to $GOBIN/$GOPATH/bin for standalone
##          PATH use. NOT used by the install-* targets (those bundle a frozen copy into
##          each host's plugin install). Only needed if you want `agent-bridge-mcp` on PATH.
install:
	go install $(CMD)

## vet: static checks
vet:
	go vet ./...

## test: run tests
test:
	go test ./...

## smoke: build + smoke-test ALL tools (gemini_agent + claude_agent + codex_agent).
##        Needs agy, claude AND codex authed; runs each in a clean temp dir. For one
##        tool, use the smoke-gemini / smoke-claude / smoke-codex targets.
smoke: smoke-gemini smoke-claude smoke-codex
	@echo "smoke OK (gemini + claude + codex)"

# Map each smoke-<label> target to the MCP tool it exercises.
TOOL_gemini := gemini_agent
TOOL_claude := claude_agent
TOOL_codex  := codex_agent

## smoke-gemini: smoke-test just gemini_agent (clean temp dir; needs agy authed)
## smoke-claude: smoke-test just claude_agent (clean temp dir; needs claude authed)
## smoke-codex: smoke-test just codex_agent (clean temp dir; needs codex authed)
smoke-gemini smoke-claude smoke-codex: smoke-%: build
	@mkdir -p /tmp/agent-bridge-mcp-smoke-$*
	@printf '%s\n' \
	'{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
	'{"jsonrpc":"2.0","method":"notifications/initialized"}' \
	'{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"$(TOOL_$*)","arguments":{"task":"Reply with exactly the word: PONG","working_dir":"/tmp/agent-bridge-mcp-smoke-$*","timeout_seconds":120}}}' \
	| ./$(BINARY) | grep -q PONG && echo "smoke-$* OK" || (echo "smoke-$* FAILED"; exit 1)

## install-claude: register this repo as a local marketplace and install the plugin into
##                 Claude Code (loads the skills AND the agent-bridge MCP server).
##                 `claude plugin install` copies the plugin — binary included — into a
##                 versioned cache (~/.claude/plugins/cache/.../$(BINARY)), so the install
##                 is a FROZEN snapshot: editing/rebuilding this checkout never changes a
##                 running agent. Re-run to push a new build. Restart Claude Code after.
##                 (Want only the tools, no skills? `claude mcp add agent-bridge --scope
##                 user -- <abs path to $(BINARY)>` — but that references the path you give,
##                 so it is not frozen.)
install-claude: build
	-claude plugin marketplace remove $(MARKETPLACE)
	claude plugin marketplace add $(CURDIR)
	claude plugin install $(PLUGIN)@$(MARKETPLACE)
	@echo "installed $(PLUGIN)@$(MARKETPLACE) into Claude Code (skills + MCP: gemini_agent + claude_agent + codex_agent, frozen cache copy — from Claude use gemini_agent or codex_agent). Restart Claude Code, then /mcp + /plugin to confirm."

## uninstall-claude: remove the plugin and its local marketplace from Claude Code
uninstall-claude:
	-claude plugin uninstall $(PLUGIN)@$(MARKETPLACE)
	-claude plugin marketplace remove $(MARKETPLACE)
	@echo "removed $(PLUGIN) and marketplace $(MARKETPLACE) from Claude Code."

## install-agy: import this repo's plugin into the Antigravity `agy` CLI, then install a
##             FROZEN copy of the MCP binary into agy's OWN plugin dir and point the
##             imported mcp_config.json at it. agy copies the plugin MANIFESTS but not the
##             binary, and has no ${CLAUDE_PLUGIN_ROOT} support (confirmed via `strings`),
##             so the command must be an absolute path. Copying into $(AGY_PLUGIN_DIR)
##             (rather than referencing this checkout) keeps the install self-contained and
##             frozen — rebuilding the checkout won't change a running agent; re-run to
##             update. Restart Antigravity after.
install-agy: build
	agy plugin install $(CURDIR)
	@if [ -d "$(AGY_PLUGIN_DIR)" ]; then \
	  cp $(CURDIR)/$(BINARY) "$(AGY_PLUGIN_DIR)/$(BINARY)" && echo "installed frozen binary -> $(AGY_PLUGIN_DIR)/$(BINARY)"; \
	else \
	  echo "WARNING: $(AGY_PLUGIN_DIR) not found; agy plugin layout may differ (pass AGY_PLUGIN_DIR=...)."; \
	fi
	@cfg='$(AGY_PLUGIN_DIR)/mcp_config.json'; \
	if [ -f "$$cfg" ]; then \
	  sed 's#$${CLAUDE_PLUGIN_ROOT}/$(BINARY)#$(AGY_PLUGIN_DIR)/$(BINARY)#' "$$cfg" > "$$cfg.tmp" && mv "$$cfg.tmp" "$$cfg" && \
	  echo "repointed agy MCP command -> $(AGY_PLUGIN_DIR)/$(BINARY)"; \
	else \
	  echo "WARNING: $$cfg not found; set the MCP command to $(AGY_PLUGIN_DIR)/$(BINARY) manually."; \
	fi
	@echo "installed $(PLUGIN) into agy (skill + MCP: gemini_agent + claude_agent + codex_agent — from agy use claude_agent). Restart Antigravity; 'agy plugin list' to confirm."

## uninstall-agy: remove this plugin (and its frozen binary) from the Antigravity `agy` CLI
uninstall-agy:
	-agy plugin uninstall $(PLUGIN)
	-rm -f "$(AGY_PLUGIN_DIR)/$(BINARY)"
	@echo "removed $(PLUGIN) from agy."

## install-codex: install this repo as a Codex plugin (skill + MCP server, bundled). Codex
##               requires a plugin's skills AND any bundled MCP binary to live INSIDE the
##               plugin root (its validator forbids `..`/symlink escapes; the bundled
##               .mcp.json resolves `./$(BINARY)` relative to the installed plugin), so the
##               canonical ./skills and the built binary are copied into plugins/$(PLUGIN)/
##               (both gitignored) before `codex plugin add` snapshots the plugin into its
##               cache. The MCP server is wired up by the plugin itself — NO `codex mcp add`
##               — and the cache copy is FROZEN: rebuilding the checkout won't change a
##               running agent; re-run to update. Restart Codex after.
install-codex: build
	@rm -rf plugins/$(PLUGIN)/skills && mkdir -p plugins/$(PLUGIN)/skills
	@cp -R skills/. plugins/$(PLUGIN)/skills/
	@cp $(CURDIR)/$(BINARY) plugins/$(PLUGIN)/$(BINARY)
	-codex plugin marketplace remove $(MARKETPLACE)
	codex plugin marketplace add $(CURDIR)
	codex plugin add $(PLUGIN)@$(MARKETPLACE)
	@echo "installed $(PLUGIN)@$(MARKETPLACE) into Codex (skill + MCP: gemini_agent + claude_agent + codex_agent, bundled & frozen — from Codex use gemini_agent or claude_agent). Restart Codex, then 'codex plugin list' + 'codex mcp list' to confirm."

## uninstall-codex: remove the bridge plugin and its local marketplace from Codex
uninstall-codex:
	-codex plugin remove $(PLUGIN)@$(MARKETPLACE)
	-codex plugin marketplace remove $(MARKETPLACE)
	@rm -rf plugins/$(PLUGIN)/skills plugins/$(PLUGIN)/$(BINARY)
	@echo "removed $(PLUGIN) from Codex."

## install-all: install into every supported host whose CLI is on PATH (claude, agy,
##              codex). Hosts whose CLI is missing are skipped; a real install failure
##              aborts. Restart each host after. (`make install` is the unrelated
##              standalone `go install`.)
install-all: build
	@for h in claude agy codex; do \
	  if command -v $$h >/dev/null 2>&1; then \
	    echo "=== install-$$h ==="; $(MAKE) --no-print-directory install-$$h || exit $$?; \
	  else \
	    echo "=== skip $$h (CLI not on PATH) ==="; \
	  fi; \
	done

## uninstall-all: remove the bridge from every supported host whose CLI is on PATH
uninstall-all:
	@for h in claude agy codex; do \
	  if command -v $$h >/dev/null 2>&1; then \
	    echo "=== uninstall-$$h ==="; $(MAKE) --no-print-directory uninstall-$$h; \
	  else \
	    echo "=== skip $$h (CLI not on PATH) ==="; \
	  fi; \
	done

## clean: remove the built binary
clean:
	rm -f $(BINARY)

## help: list targets (one line each; see the Makefile for full descriptions)
help:
	@grep -E '^## [^ ]' $(MAKEFILE_LIST) | sed 's/## //'

BINARY := agent-bridge-mcp
# CMD is the MCP server's main package (module: github.com/adubkov/agent-bridge).
CMD    := ./cmd/$(BINARY)

MARKETPLACE := agent-bridge-local
PLUGIN      := agent-bridge

# Where the Antigravity CLI copies imported plugins. agy imports the plugin
# MANIFESTS but not the built binary, and doesn't expand Claude's
# ${CLAUDE_PLUGIN_ROOT}, so install-agy repoints the imported mcp_config.json at
# the absolute repo binary. Override AGY_PLUGIN_DIR if your agy layout differs.
AGY_PLUGIN_DIR := $(HOME)/.gemini/config/plugins/$(PLUGIN)

.PHONY: build install vet test clean smoke smoke-gemini smoke-claude smoke-codex install-claude uninstall-claude install-agy uninstall-agy install-codex uninstall-codex plugin-install plugin-uninstall help

## build: compile the MCP server (cmd/agent-bridge-mcp) into the REPO ROOT
##        (./agent-bridge-mcp). This is the canonical artifact: the plugin's
##        .mcp.json (${CLAUDE_PLUGIN_ROOT}/agent-bridge-mcp) and `make install-claude`
##        ($(CURDIR)/agent-bridge-mcp) both reference it.
build:
	go build -o $(BINARY) $(CMD)

## install: OPTIONAL — `go install` the MCP server to $GOBIN/$GOPATH/bin for standalone
##          PATH use. NOT used by the plugin or install-claude (those use the repo-dir
##          binary from `make build`). Only needed if you want `agent-bridge-mcp` on PATH.
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

## install-claude: register the MCP server with Claude Code (user scope) via `claude mcp add`
install-claude: build
	claude mcp add agent-bridge --scope user -- $(CURDIR)/$(BINARY)
	@echo "registered 'agent-bridge' MCP server (tools: gemini_agent + claude_agent + codex_agent — from Claude use gemini_agent or codex_agent). Restart Claude Code, then /mcp to confirm."

## uninstall-claude: remove the MCP server registration from Claude Code
uninstall-claude:
	-claude mcp remove agent-bridge --scope user
	@echo "removed 'agent-bridge' MCP server registration."

## plugin-install: register this repo as a local marketplace and install the plugin
##                  (loads BOTH the agent-bridge skill and the agent-bridge MCP server).
##                  Requires .claude-plugin/marketplace.json. Restart Claude Code after.
plugin-install: build
	-claude plugin marketplace remove $(MARKETPLACE)
	claude plugin marketplace add $(CURDIR)
	claude plugin install $(PLUGIN)@$(MARKETPLACE)
	@echo "installed $(PLUGIN)@$(MARKETPLACE) (skill: agent-bridge, MCP: agent-bridge). Restart Claude Code, then /mcp + /plugin to confirm."

## plugin-uninstall: remove the plugin and its local marketplace
plugin-uninstall:
	-claude plugin uninstall $(PLUGIN)@$(MARKETPLACE)
	-claude plugin marketplace remove $(MARKETPLACE)
	@echo "removed $(PLUGIN) and marketplace $(MARKETPLACE)."

## install-agy: register this repo's plugin with the Antigravity `agy` CLI, then
##              repoint the imported MCP server at the absolute repo binary. agy
##              copies the plugin MANIFESTS but NOT the built binary, and its
##              binary has no ${CLAUDE_PLUGIN_ROOT} support at all (confirmed via
##              `strings`), so the imported mcp_config.json MUST use an absolute
##              command path. We point at the live repo binary ($(CURDIR)/$(BINARY))
##              rather than copying it into agy's dir: a copy would be a stale
##              snapshot needing re-install on every `make build`, whereas the
##              reference picks up rebuilds automatically (and matches how
##              install-claude registers the same canonical binary). The trade-off
##              is that the registration is tied to this checkout's location.
##              Restart Antigravity after.
install-agy: build
	agy plugin install $(CURDIR)
	@cfg='$(AGY_PLUGIN_DIR)/mcp_config.json'; \
	if [ -f "$$cfg" ]; then \
	  sed 's#$${CLAUDE_PLUGIN_ROOT}/$(BINARY)#$(CURDIR)/$(BINARY)#' "$$cfg" > "$$cfg.tmp" && mv "$$cfg.tmp" "$$cfg" && \
	  echo "repointed agy MCP command -> $(CURDIR)/$(BINARY)"; \
	else \
	  echo "WARNING: $$cfg not found; agy plugin layout may differ. Set the MCP command to $(CURDIR)/$(BINARY) manually (or pass AGY_PLUGIN_DIR=...)."; \
	fi
	@echo "installed $(PLUGIN) into agy (MCP: agent-bridge; tools: gemini_agent + claude_agent + codex_agent — from agy use claude_agent). Restart Antigravity; 'agy plugin list' to confirm."

## uninstall-agy: remove this plugin's registration from the Antigravity `agy` CLI
uninstall-agy:
	-agy plugin uninstall $(PLUGIN)
	@echo "removed $(PLUGIN) from agy."

## install-codex: register the bridge with Codex — the MCP tools via `codex mcp add`
##               AND this repo's skills via a local Codex plugin marketplace
##               (.agents/plugins/marketplace.json + plugins/$(PLUGIN)/.codex-plugin).
##               Codex requires a plugin's skills to live INSIDE the plugin root (its
##               validator forbids `..`/symlink escapes), so the canonical ./skills are
##               copied into ./plugins/$(PLUGIN)/skills (gitignored) first. The MCP
##               command is the absolute repo binary (Codex has no ${CLAUDE_PLUGIN_ROOT}),
##               matching install-claude/install-agy. Restart Codex after.
install-codex: build
	@rm -rf plugins/$(PLUGIN)/skills && mkdir -p plugins/$(PLUGIN)/skills
	@cp -R skills/. plugins/$(PLUGIN)/skills/
	-codex mcp remove agent-bridge
	codex mcp add agent-bridge -- $(CURDIR)/$(BINARY)
	-codex plugin marketplace remove $(MARKETPLACE)
	codex plugin marketplace add $(CURDIR)
	codex plugin add $(PLUGIN)@$(MARKETPLACE)
	@echo "registered 'agent-bridge' with Codex (MCP: gemini_agent + claude_agent + codex_agent — from Codex use gemini_agent or claude_agent; skill: multi-model-review). Restart Codex, then 'codex mcp list' + 'codex plugin list' to confirm."

## uninstall-codex: remove the bridge from Codex (MCP tools, plugin, and marketplace)
uninstall-codex:
	-codex plugin remove $(PLUGIN)@$(MARKETPLACE)
	-codex plugin marketplace remove $(MARKETPLACE)
	-codex mcp remove agent-bridge
	@rm -rf plugins/$(PLUGIN)/skills
	@echo "removed $(PLUGIN) from Codex."

## clean: remove the built binary
clean:
	rm -f $(BINARY)

## help: list targets (one line each; see the Makefile for full descriptions)
help:
	@grep -E '^## [^ ]' $(MAKEFILE_LIST) | sed 's/## //'

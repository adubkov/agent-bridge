BINARY := agy-mcp
PKG    := github.com/adubkov/agy-mcp

.PHONY: build install vet test clean smoke install-claude uninstall-claude plugin-link help

## build: compile the MCP server binary into the repo root (referenced by .mcp.json)
build:
	go build -o $(BINARY) .

## install: go install the binary into $GOBIN / $GOPATH/bin
install:
	go install .

## vet: static checks
vet:
	go vet ./...

## test: run tests
test:
	go test ./...

## smoke: build + drive the stdio server through initialize + a reason-only tools/call
##        (runs agy in a clean temp dir so it doesn't scan this repo; needs agy authed)
smoke: build
	@mkdir -p /tmp/agy-mcp-smoke
	@printf '%s\n' \
	'{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
	'{"jsonrpc":"2.0","method":"notifications/initialized"}' \
	'{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"gemini_agent","arguments":{"task":"Reply with exactly the word: PONG","working_dir":"/tmp/agy-mcp-smoke","timeout_seconds":120}}}' \
	| ./$(BINARY) | grep -q PONG && echo "smoke OK" || (echo "smoke FAILED"; exit 1)

## install-claude: register the MCP server with Claude Code (user scope) via `claude mcp add`
install-claude: build
	claude mcp add agy --scope user -- $(CURDIR)/$(BINARY)
	@echo "registered 'agy' MCP server (tool: gemini_agent). Restart Claude Code, then /mcp to confirm."

## uninstall-claude: remove the MCP server registration from Claude Code
uninstall-claude:
	-claude mcp remove agy --scope user
	@echo "removed 'agy' MCP server registration."

## plugin-link: symlink this repo into the Claude Code plugins dir (registers MCP + skill)
plugin-link: build
	mkdir -p $(HOME)/.claude/plugins
	ln -sfn $(CURDIR) $(HOME)/.claude/plugins/agy-gemini
	@echo "linked $(CURDIR) -> ~/.claude/plugins/agy-gemini (restart Claude Code to load)"

## clean: remove the built binary
clean:
	rm -f $(BINARY)

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'

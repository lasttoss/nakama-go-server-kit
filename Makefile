.PHONY: test race cover vet fmt

# The whole point of this package: the tests do not need a server, a database or a socket.
test:
	go test ./...

race:
	go test -race -count=1 ./...

cover:
	go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1

vet:
	go vet ./...

fmt:
	@echo "files gofmt would change (want none):"; @gofmt -l . || true

# Three runs, because a tick loop tested against the wall clock is a tick loop tested against the
# machine it runs on. Nothing here sleeps for time to pass.
flaky:
	go test -race -count=3 ./match/

# The run CI does, on the toolchain go.mod asks for rather than the one on your PATH: CI installs that
# version, and a test that only passes on a newer one is not evidence. It is how the race in
# TestRunReturnsWhenTheMatchIsOver was found - Go 1.27 on this machine had been hiding it.
GO_MINOR := $(shell sed -n 's/^go \([0-9]*\.[0-9]*\).*/\1/p' go.mod)
test-ci:
	GOTOOLCHAIN=go$(GO_MINOR).0 go test -race -count=1 -timeout 120s ./...

# Sources are HTML and Mermaid; a PNG is a build artifact.
diagram:
	@if command -v chromium >/dev/null 2>&1; then B=chromium; elif command -v google-chrome >/dev/null 2>&1; then B=google-chrome; else echo "no chromium on PATH: open docs/diagrams/*.html in a browser"; exit 0; fi; \
	for f in docs/diagrams/*.html; do $$B --headless --screenshot="$${f%.html}.png" --window-size=1200,1000 "$$f" && echo "wrote $${f%.html}.png"; done

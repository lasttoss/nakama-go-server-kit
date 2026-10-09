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

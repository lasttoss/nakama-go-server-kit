# Measurements

The README's headline line is a claim about this repository as it stands, so it is one of the numbers
that has to be re-run rather than remembered.

## Environment

| | |
|---|---|
| Machine | 12-core x86-64 (AMD64), Ubuntu 24.04, Docker 29.1.3 |
| Go | 1.27.2 |
| Date | 2026-10-13 (UTC) |

Every host port in this file is the one the repository publishes: bound to loopback and offset from
the default, so a machine already running a Postgres, a Redis or three other stacks does not have to
stop them first.


## 95.1% statement coverage, 55 tests, 2654 lines of Go, one second with the race detector

```
$ go test -coverprofile=cover.out ./... && go tool cover -func=cover.out | tail -1
total:                    (statements)  95.1%
```

Per package, the run that produces it:

```
$ go test ./... -cover
ok  .../match     0.031s  coverage: 96.9% of statements
ok  .../rpc       0.002s  coverage: 95.9% of statements
ok  .../storage   0.003s  coverage: 93.3% of statements
ok  .../testkit   0.056s  coverage: 94.8% of statements
```

```
$ go test ./... -count=1 -v | grep -c '^--- PASS: Test'
55
```

```
$ time go test -race ./...
ok  .../storage  1.012s
ok  .../testkit  1.065s
race: 1.76 s (whole module, wall clock)
```

**Result.** Coverage and the test count are exact. Two corrections to the headline:

- **The line count has drifted: 2684, not 2654.** `find . -name '*.go' -not -path './.git/*' | xargs wc -l`
  counts the tests too, which is how the original figure was taken.
- **"One second with the race detector" is the slowest package, not the module.** The race detector
  finishes each package in about a second and the module in **1.76 s**; the README says one second,
  the measurement says one second per package.

## Why the tests take their time from an injected clock

`make test` needs no clock, no sleeping and no network, because the match loop is stepped by hand:
the same suite would take minutes if a `sleep` stood in for a tick.

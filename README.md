# nakama-go-server-kit

A fixed-tick match loop for game servers written in Go - and the test kit that makes it checkable
without a running server.

```go
loop, err := match.Start(ctx, match.Options{
    TickRate: 30,
    OnTick:   room.Step,          // func(ctx context.Context, dt time.Duration)
    Logger:   logger,
})
if err != nil {
    return err
}
stats := loop.Wait()
```

Tested with a clock the test owns, so the interesting cases take milliseconds rather than a server:

```go
clk := testkit.NewFakeClock()
loop, _ := match.Start(ctx, match.Options{TickRate: 30, Clock: clk, OnTick: room.Step})

clk.AdvanceByTicks(3)                       // three ticks, right now
if loop.Snapshot().Ticks != 3 { ... }
```

**95.0% statement coverage, 21 tests, 1140 lines of Go, one second with the race detector**
(`go test -race ./...`). No dependencies: `go.mod` has no `require` lines at all.

## The three problems this is here to solve

**A tick that runs long is a stutter, and nobody knows which handler did it.** `Loop` measures every
tick against its budget, counts the ones that overran, keeps a p95 over the recent ticks, and calls
`OnSlowTick` so a room can shed load before the players notice. `Stats` is there for whatever
exposes metrics.

**Timing code is normally untestable, so it ends up untested.** The loop takes its passage of time
from a `Clock` interface. A test hands it a fake one, steps it by hand, and asserts on ticks that
really happened instead of sleeping and hoping.

**A panic in a handler takes the whole server with it.** In a game runtime one unexpected message
from one player should cost that room a tick, not every room on the process. The recovery lives in
the loop, and there is a test that panics in a tick and then checks the loop is still running.

## What is in the box

| Package | What it does |
|---|---|
| `match` | `Start` / `Run`, a fixed-rate tick with a measured budget, overrun counting, `Percentile`, a `Stats` any goroutine can read, and a panic that stays in its tick |
| `testkit` | `FakeClock` (`Advance`, `AdvanceByTicks`, `Jump`, `Tickers`) and `WaitFor`, so a test can move time and observe the result |

Two packages and no dependencies at all: `go.mod` has no `require`, and the module graph is empty.
That is deliberate - this is the part of a game server that should be inspectable in one sitting.

## The clock, and the two ways to move it

A test that wants *ticks* uses `AdvanceByTicks(n)`: it waits for the loop to take each tick before
firing the next, so none is lost and "after three ticks" means three ticks.

A test that wants to check *behaviour under load* uses `Advance(d)`: a tick the loop was not ready to
receive is dropped and counted by the ticker, exactly as a real `time.Ticker` drops one, which is why
the loop counts what it missed rather than pretending it did not. There is a test for that too.

`Jump(d)` moves the clock without firing a tick, which is how a test makes one handler look slow:

```go
OnTick: func(context.Context, time.Duration) { clk.Jump(30 * time.Millisecond) }, // 30ms of a 33ms budget
OnSlowTick: func(tick uint64, took time.Duration) { slow = append(slow, tick) },
```

## Notes from building it

- **The ticker is registered in `Start`, not in the goroutine.** Otherwise the first interval is
  lost to a race with the scheduler, and a test that starts a loop and immediately moves the clock
  discovers it. The same race exists in production; it is just harder to see.
- **`dt` is measured from `Start`.** A tick delivered into a buffer while the loop was still starting
  up must not be reported as zero game time, or the simulation steps by nothing.
- **`Percentile(p)` is nearest-rank**: the p95 of ten ticks is the slowest of the ten. An operator
  asking about p95 is asking which tick an unlucky player felt, so the answer should be a tick time
  that really happened rather than an interpolation.
- **`Stats` is readable while the loop runs**, under a mutex held only for the assignments: a handler
  that calls `Snapshot` from inside `OnTick`, and a dashboard in another goroutine, both have to
  work. `go test -race` is what proves it.
- **The fake clock had a race of its own** - it read the current instant without the lock while
  handing a tick over - and `-race` found it the moment the test suite grew. A test kit is code that
  people trust; it gets tested here like anything else.

## What this is not

Not a matchmaking service, not a game framework, and not a Nakama dependency: it does not import
`nakama-common` at all. The loop only needs a function to call and a clock, so it fits an
authoritative match, a room in a Zinx server, or a bot harness - and it can be tested without any of
them.

Next: the same treatment for the two other places a Nakama module gets awkward - storage writes that
have to survive two players editing the same object at once, and RPC handlers that must validate
their input - both against in-memory fakes in `testkit`.

## License

MIT. See `LICENSE`, and `PROVENANCE.md` for where this code comes from.

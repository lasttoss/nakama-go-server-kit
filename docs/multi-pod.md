# More than one pod: what Nakama OSS does not do for you

Nakama's own storage is shared, so *data* is correct on any number of pods. What is not shared is
anything that lives in a node's memory — and three things do. Each one fails quietly.

| Failure | Why it happens in OSS | What fixes it |
|---|---|---|
| A player on pod B cannot join a match that lives on pod A | A match runs on exactly one node; presence elsewhere is remote | **match relay** over the cache's pub/sub |
| The old device is not logged out when the same account signs in on a new machine | The session registry is per-node RAM; `SessionDisconnect` is local | a **cross-pod kick bus** plus a local session book |
| `SessionDisconnect` returns OK and the socket stays open | `sessionID` is *not* in the session token (the claim is the token id) | take the id from `runtime.Presence.GetSessionId()` |

So there are two paths, and they need different mechanisms:

```
pod A (holds the old device's socket)              pod B (new device signs in / kick RPC)
 book: uid -> sessionID (GetSessionId)              KickByUID(uid, disconnect):
 listens on <app>:ctrl:kick   <---- cache ---       (1) if this pod owns the socket, drop it locally
   -> look up the book -> SessionDisconnect(sid)    (2) publish {uid, sid, from: podB}
```

Two rules that make it work: a pod ignores messages whose `from` is itself (otherwise pods kick each
other in a loop), and the book is written by the cheapest hooks that have a `runtime.Presence`
(`MatchJoin`, plus logout) because a device that never joined a match is otherwise invisible.

## Measured on two pods

| players | errors | CPU / pod | RAM / pod | cache connections / pod | p50 auth / join |
|---|---|---|---|---|---|
| 300 | 0 | ~0.45 core | ~170 MiB | ~150 | 170 / 282 ms |
| 1,000 | 0 | ~1.0 core (68% of a 1500m cap) | ~600 MiB | ~510 | 241 / 435 ms |
| 2,000 | 139 — **all of them business errors** (`match full`) | 1.018 / 1.004 core | 1.122 / 0.861 GiB | 813 / 806 | 941 / 1,193 ms |

Extrapolating: **≈0.5 core per 1,000 players per pod**, so 5,000 players needs about 2.5 cores per
pod and 2 pods is ~5 cores — a 4 vCPU node is not enough. The kick itself was measured end to end:
the socket on the other pod closed in **under a second**, immediately after the RPC returned.

## Runbook

| Symptom | Usual cause | Check |
|---|---|---|
| a client on pod B cannot enter a match | relay disabled, or the pods point at **different cache databases** | `printenv` in **both** pods, then the relay's own log lines |
| a player on another pod loses input silently | de-duplication done globally instead of per source | the per-source sequence filter; test two sources with the same sequence number |
| `SessionDisconnect` succeeds and the socket lives | a token id was passed instead of a session id | use `Presence.GetSessionId()` and check the book is non-empty |
| `max number of clients reached` | a cache client created per match | one client per `(address, db)`, bounded pool, `SCAN` not `KEYS` |
| `expects valid session id` | empty or malformed id (empty book) | write the book from several hooks, not just one |

## The lessons that cost the most

- **`helm upgrade` reported "2 new pods updated" while the environment variables never landed.**
  Confirm with `printenv` *inside the pod after the upgrade*, not by reading the values file; an
  `env:` list in a template can be silently overridden by another values file.
- **A session id is not a token id.** The token carries a token id; passing it to
  `SessionDisconnect` is a silent no-op that looks like success.
- **De-duplicate per source, never globally** — otherwise players on other pods lose input with no
  error in any log.
- **One cache client per process**, shared by every package that touches the cache, cached by
  `(address, db)`.

## Porting checklist

- [ ] Nakama OSS recent enough to have `Presence.GetSessionId()`.
- [ ] One shared cache, the **same database** on every pod; `maxclients` and `timeout` sized.
- [ ] Relay and kick bus enabled on **every** pod, verified with `printenv` *and* the boot log.
- [ ] Hooks registered on every pod; the relay has heartbeat, close and a watchdog for a silent owner.
- [ ] De-duplication per source; backpressure bounded and drops counted.
- [ ] Session book written from `runtime.Presence` on `MatchJoin`, cleared when the session leaves.
- [ ] Bus is nil-safe (a nil bus behaves exactly like a single pod) and its logger survives panics.
- [ ] Tests: unit (de-dup, payload parsing, nil-safety), bus-against-a-real-cache, and a two-pod end-to-end that **measures the kick latency**.

# Provenance

Everything in this repository was written for it, on personal time, by Tran Trung Dung.

- No employer code, no client code, no proprietary data. Nothing here is extracted from a product I
  worked on; the API shapes it talks about are the public documentation of the game server this is
  meant to sit behind.
- No third-party source is vendored. The only dependency is the Go standard library, and the module
  graph is therefore empty: `go.mod` has no `require` lines at all.
- The diagrams, if any, are drawn here. The commit history is the history of this repository.
- The measured numbers quoted in the README come from running the harness in this repository, and the
  command that produced each one is next to it.

If you believe something here belongs to you, open an issue and it will be dealt with.

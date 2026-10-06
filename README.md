# GoCapsule

GoCapsule is a small, offline-first incident recorder for Go services. It keeps
a bounded `runtime/trace` flight-recorder window and, when a stable trigger
fires, atomically writes that recent history together with point-in-time pprof
profiles and allowlisted scalar runtime metrics.

The project is intentionally **not** an observability backend. It has no server,
agent, upload, database, or UI. A capsule is a local `tar.gz` file that standard
Go tools can read after you verify and extract it.

## Why it exists

A goroutine profile answers “what exists now.” A flight-recorder trace also
contains recent scheduling history. A short-lived goroutine burst can disappear
before a profile is captured while still being visible in the historical trace.
GoCapsule puts both views in one integrity-checked, size-limited artifact.

## Quick start

Go 1.25 or newer is required because GoCapsule uses `runtime/trace.FlightRecorder`.

```sh
go test ./...
go run ./cmd/gocapsule demo -output ./gocapsules
go run ./cmd/gocapsule verify ./gocapsules/gocapsule-*.tar.gz
go run ./cmd/gocapsule inspect ./gocapsules/gocapsule-*.tar.gz
```

Verify before extraction, especially when an archive came from another machine.
After extracting into a new private directory:

```sh
go tool trace trace.out
go tool pprof profiles/goroutine.pb.gz
go tool pprof profiles/heap.pb.gz
```

Embedding it in a service is explicit:

```go
recorder, err := capsule.NewRecorder(capsule.Config{OutputDir: "/private/incidents"})
if err != nil { /* handle */ }
if err := recorder.Start(); err != nil { /* handle */ }
defer recorder.Close()

result, err := recorder.Capture(ctx, "worker.queue-depth")
```

Only one Go flight recorder can be active in a process. Capture reason codes are
not free-form labels: they must start with a lowercase ASCII letter and then use
only lowercase letters, digits, `.`, `_`, or `-` (64 bytes maximum). Expected
concurrent or cooldown suppression is returned as an `Outcome`; collection and
I/O failures are errors.

## Capsule layout

```text
manifest.json
trace.out
metrics.json
profiles/goroutine.pb.gz
profiles/heap.pb.gz
```

The manifest records schema/version, platform metadata, the reason code, and a
size plus SHA-256 digest for every payload. Digests detect accidental corruption;
they do **not** prove who created a capsule.

Collection uses hard byte limits for the trace snapshot, each collected payload,
the total payload bytes, and the final compressed archive. The runtime flight
recorder's `MaxBytes` is only a Go runtime hint: it does not guarantee a memory
ceiling or bound what `WriteTo` emits. GoCapsule therefore also bounds the data it
collects, but it does not claim that total process memory overhead is hard-bounded.
Any overflow or write failure removes the temporary file and publishes nothing.

Archives are written through a same-directory temporary file and rename. Newly
created output directories use mode `0700` and archives use `0600` on Unix.
Existing directory permissions are never changed.

The output path must be stable and trusted, and it must not be group- or
world-writable. GoCapsule deliberately does not change an existing directory's
permissions, so operators must validate and provision that directory securely.
GoCapsule bounds each individual artifact but does not implement retention.
Operators **must** configure external rotation by count, age, and total bytes;
otherwise disk use and sensitive-data accumulation remain unbounded over time.

## Trigger policy

`GoroutineThreshold` is a small hysteresis policy. It fires only after observing
a count below its rearm limit followed by a count at or above the threshold, and
does not fire again until it has rearmed. `WatchGoroutines` connects the policy to
`runtime.NumGoroutine` and a ticker; the policy itself is deterministic and can be
unit tested without time or runtime dependencies.

## Threat model and constraints

Capsules are sensitive diagnostic data. Traces and profiles can reveal function
names, module names, stack structure, timings, and operational behavior. Capture
is whole-process, not request- or tenant-scoped. It may also contain application
`trace.Log` category/message values and pprof label values. Secrets must never be
placed in trace messages, categories, or pprof labels. Workloads requiring tenant
isolation must enforce it at the process boundary. GoCapsule never uploads
artifacts and accepts no arbitrary manifest metadata, but it cannot redact Go
trace or pprof internals without making those standard formats invalid. Store
capsules like crash dumps, restrict access, and review them before sharing.

The verifier reads without extracting, applies compressed, per-entry, and total
limits, and rejects unsafe paths, non-regular entries, duplicates, missing or
unlisted payloads, schema errors, and digest/size mismatches. This reduces archive
parser risk; it is not a malware scanner or authenticity system.
For nested `profiles/*.pb.gz` payloads, verification checks bounded gzip
decompression and gzip CRC/size integrity; it does not claim to validate pprof
protobuf semantics. The integration test confirms that artifacts generated by
GoCapsule are accepted by the standard `go tool trace` and `go tool pprof` tools.

GoCapsule does not diagnose root cause, continuously profile production, replace
telemetry, or make capturing heap/profile data free. Block and mutex profiles are
optional and GoCapsule never changes their global sampling rates.

## Development

```sh
gofmt -w .
go vet ./...
go test ./...
go test -race ./...
go build ./...
```

## License

MIT

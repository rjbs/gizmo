# Learning Go from Gizmo

A self-guided curriculum for an experienced programmer, strong in Perl, fluent
in Rust, and at home in JMAP, learning Go by reading Gizmo, a library for
writing JMAP servers, and the servers built on it.  The aim is to be able to
read, understand, test, and edit these systems, and other Go code besides.

## How This Works

Gizmo is the spine.  Its packages are small, each has one job, and they depend
on one another in an order that is close to a teaching order.  Three servers
are built on it, and they supply what a library can't:

- **TaskMaster** is the standing example of an application: the smallest of
  the three, and the one that shows Gizmo's hooks from the side that fills
  them in.
- **Jiggle** is where an application does its own concurrent work, runs other
  programs, and embeds files.
- **Juke** is the oldest and largest, and is drawn on now and then.

Each unit lives in its own file in this directory and follows this structure:

- **Introduction:** what the unit covers and why it comes now
- **Concepts:** explanation tied to how the concept appears in the code, with
  notes on what is surprising coming from Perl or from Rust
- **Reading:** links to go.dev, the standard library's documentation, and
  other resources
- **Code tour:** specific files and line ranges to read and understand
- **Exercises:** optional hands-on experiments.  Most are made in
  `internal/bakery`, Gizmo's own small test server, where nothing real can
  be broken.

The syllabus comes first.  After it, each unit is written only when the
reader is ready for it, then read and questioned.  The answers are folded
back into the unit, and steer the writing of the next.

## What the text was written against

The code will move.  Each unit names the commit of each repository it was
written against, so that a reader who finds the text and the code disagreeing
can see where the code has drifted from.  The syllabus was written against:

| Repository | Commit |
|---|---|
| gizmo | `f549a2d` (after `v0.1.4`) |
| TaskMaster | `98064bb` |
| Jiggle | `07f1763` |
| Juke | `6791bd0` |

---

## Units

### [1. Go as a Language](unit-01-go-language.md)

The language before the libraries, by way of the plainest code in Gizmo.
Packages and files, and how a directory is a package.  Capitalization as
access control: there is no `pub`.  Declarations, `:=`, and zero values in
place of constructors and `undef`.  Slices and maps, and what `make` and
`append` do.  Multiple return values and the `value, ok` idiom.  `any`, type
switches and type assertions, for values whose type is known only at run time:
decoded JSON, here.  What is absent, coming from Rust: no sum types, no
`Option`, no `Result`, no ownership.  The toolchain as far as reading needs
it: `go build`, `go test`, `go vet`, `go doc`, and `gofmt` as the end of all
arguments about style.

**Files to look at:** `jmap/pointer.go`, `jmap/patch.go`, `jmap/limits.go`

---

### 2. Types, Methods, and Errors

Structs, and methods as functions with a receiver.  Pointer receivers and
value receivers, and how to tell which one a type wants.  Pointers without
pointer arithmetic or lifetimes, and what `nil` can be.  Embedding, which
looks like inheritance and is not.  Then errors: `error` is a small
interface, an error is an ordinary value, and `if err != nil` is the whole of
the control flow.  Gizmo's three error types, and why some functions return
`*SetError` and not `error`.  `errors.As` and `errors.Is` against a wrapped
chain, and `%w`.  `defer`, as the nearest thing to `Drop`.  `panic`, and why
it is so seldom the answer.

**Files to look at:** `jmap/errors.go`, `std/std.go` (`Work.Do`, `Transact`),
`blob/blob.go` (`Refusal`), `sqlitekit/tx.go` (the `Tx` struct)

---

### 3. JSON and Struct Tags

`encoding/json` is the first standard library package worth knowing in
depth, and a JMAP server is made of it.  Struct tags and reflection: Go's
answer to `serde` derive, done at run time.  Decoding into a struct, into a
map, and into `any`, and what numbers become.  Pointers and `omitempty` for
"absent", which Go otherwise can't tell from zero.  `json.RawMessage` to put
off decoding until something knows the type.  `MarshalJSON` and
`UnmarshalJSON`, by which an `Invocation` is a three-element array on the
wire and a struct in the program.  Strict decoding, and how `std` splits a
method's arguments between the standard ones and a type's own.

**Files to look at:** `jmap/invocation.go`, `std/std.go` (`decode`, `strict`,
`fieldNames`), `std/get.go` and `std/query.go` (the request structs)

---

### 4. Functions as Values, and Context

Function types, closures, and a map from names to functions: the method
registry.  How a request travels through `Processor.Process`, call by call,
with result references and creation ids resolved along the way.  Then
`context.Context`, which is the first argument of nearly every function in
these programs: what it carries (cancellation, deadlines, and a few values),
why it is passed and not stored, and the unexported-key idiom for context
values.  Functional composition in a small place: `auth.ForAccount`, a
generic function that takes two functions and returns a `jmap.Handler`.

**Files to look at:** `jmap/processor.go`, `auth/route.go`, `auth/auth.go`
(`WithUser`, `UserFrom`)

---

### 5. Testing

The `testing` package and `go test`: no framework, no assertions, and what
is done in their place.  `_test.go` files, and the difference between
`package jmap` and `package jmap_test`.  `t.Helper`, `t.Cleanup`, `t.Fatal`
against `t.Error`.  The style these repositories use: each case is one call
to a named helper that takes its inputs and what is expected.  Table-driven
tests and `t.Run` subtests, the commoner style elsewhere, as Jiggle has
them.  `httptest`, for a real server on a real socket inside a test.  The
bakery as a fixture, and `jmaptest` as a library written for tests, modeled
on JMAP::Tester.  Running one test, reading a failure, `-v`, `-run`,
`-count=1`, and coverage.

**Files to look at:** `jmap/patch_test.go`, `jmap/processor_test.go`,
`std/std_test.go`, `jmaptest/tester.go`, `jmaptest/response.go`,
`internal/bakery/`; in Jiggle, `internal/server/server_test.go`

---

### 6. Interfaces and Generics

Interfaces are satisfied by having the methods, with nothing declared: a
trait with no `impl` line.  Small interfaces, defined by the code that uses
them, not by the code that satisfies them.  What an interface value is at
run time, and the nil interface that isn't nil.  Then generics: type
parameters and constraints, and how far they are from Rust's (no associated
types, no specialization, no methods with type parameters of their own).
`std` is the case study.  `Tx` is an interface, yet `Begin[T Tx]`,
`Work[T]` and `Getter[T]` are generic over it: why both, and what an
application would lose if `std` took the interface alone.  A data type
described by a struct of functions, where another design would have asked
for an interface, and the trade between the two.  Type aliases against type
definitions.

**Files to look at:** `std/std.go`, `std/get.go`, `std/set.go`,
`std/query.go`, `std/changes.go`, `internal/bakery/types.go`

---

### 7. An Application on Gizmo

The same hooks from the other side.  How TaskMaster is laid out: `cmd/` for
the program, `internal/` for everything else, and what `internal` means to
the compiler.  A real type, the Task, filling in `Getter`, `Setter` and
`Querier` with its own transaction type as `T`.  Where validation lives and
how a `SetError` gets from a hook to the response.  Objects as
`map[string]any` against objects as structs, and why a server for an
open-ended draft specification chose as it did.  Range-over-func iterators
(`iter.Seq2`), Go's newest way to write a loop.  A custom method beside the
standard ones.

**Files to look at:** in TaskMaster, `internal/tasks/std.go`,
`internal/tasks/task.go`, `internal/tasks/service.go`,
`internal/tasks/alerts.go`, `internal/server/server.go`

---

### 8. SQL: `database/sql` and sqlitekit

`database/sql` is DBI's nearer cousin: a pool of connections behind
`*sql.DB`, drivers registered by a blank import, and `Query`, `Scan`,
`Exec`.  Rows that must be closed, `sql.ErrNoRows`, and NULL.  Transactions,
and why a `*sql.Tx` matters more in Go than a `$dbh` in a transaction does
in Perl: the pool.  The `Queryer` interface, one small interface that lets
a function take a database or a transaction.  Embedding earns its keep:
`sqlitekit.Tx` is a `*sql.Tx` with more on it.  Savepoints by closure.
Migrations as an appended list.  The state, modseq and tombstone
conventions as code, and `sqlitekit.Tx` satisfying `std.Tx` without either
package naming the other's type.  A pure-Go SQLite, and what cgo would have
cost.

**Files to look at:** `sqlitekit/kit.go`, `sqlitekit/tx.go`,
`sqlitekit/kit_test.go`; in TaskMaster, `internal/store/store.go`,
`internal/store/schema.go`

---

### 9. HTTP: `net/http`

The standard library's HTTP server is the one everybody uses.
`http.Handler` is an interface of one method, and `http.HandlerFunc` is the
trick that makes a function satisfy it.  `ServeMux` and its method-and-path
patterns.  Middleware as a function from handler to handler: `Bearer`,
`Loopback`, `Tailscale` and `Guard` each wrap the next.  A goroutine per
request, and what that means for anything shared.  How Gizmo's `Server` is
itself a handler for the application to mount, and never owns the listener.
Writing a response: headers, status, body, in that order and no other.
The session resource and the API endpoint, end to end.

**Files to look at:** `httpd/httpd.go`, `httpd/guard.go`, `auth/auth.go`,
`auth/token.go`; in TaskMaster, `internal/server/server.go`

---

### 10. Streams: `io`, Blobs, and the Client

`io.Reader` and `io.Writer`, the two interfaces the rest of the standard
library is built from, and why almost nothing takes a filename.  Wrapping a
reader to change what it does: the `capped` reader that turns an oversized
upload into an error.  `io.Copy`, `io.ReadCloser`, and who closes what.
Hashing a stream as it goes to disk.  Files and temporary files in `os`.
The same streams from the other end, in `client`: `net/http` as a client,
request bodies, response bodies that must be closed, and errors a server
sends turned back into Go errors.

**Files to look at:** `blob/blob.go`, `httpd/blobs.go`, `client/client.go`,
`client/blob.go`, `client/batch.go`, `internal/bakery/pantry.go`

---

### 11. Concurrency

Goroutines, channels and `select`.  Go's bet is the opposite of Rust's:
concurrency is easy to start and the compiler will not stop a data race,
so the discipline is the programmer's, with the race detector's help.
`sync.Mutex` around shared maps, as in the push broker.  A buffered channel
as a semaphore, as in the upload limiter.  A channel of `struct{}` as a
signal with no data.  `select` with `ctx.Done()`, the shape of every loop
that must be able to stop.  The event source: one goroutine per connected
client, parked until there is news.  Then an application's own workers:
Jiggle's lanes of image and video work, and a `sync.WaitGroup` over
uploads.  Goroutine leaks, and how the tests guard against them.

**Files to look at:** `push/push.go`, `httpd/eventsource.go`,
`httpd/blobs.go` (`limiter`), `jmaptest/events.go`; in Jiggle,
`internal/server/worker.go`, `internal/publish/bunny.go`

---

### 12. Programs

What a library never has: `package main`.  `flag`, and subcommands built by
hand.  Configuration read from JSON and checked before anything starts.
`log/slog` for structured logging.  Starting several listeners, waiting on
a channel of errors, and shutting down on a signal.  `//go:embed`, by which
a web client or a directory of templates is compiled into the binary.
`os/exec`, for running vips, ffmpeg and rsync, with a context to kill them.
Building: one static binary, cross-compiled by setting two environment
variables, and what that does to deployment.  How each of the three servers
mounts Gizmo beside what else it serves.

**Files to look at:** in TaskMaster, `cmd/taskmaster/main.go`,
`internal/config/config.go`, `demo-client/taskblaster.go`; in Jiggle,
`cmd/jiggle/`, `share/share.go`, `internal/derive/make.go`; in Juke,
`cmd/juke/`, `Makefile`

---

### 13. Modules, and Editing a Library Others Depend On

`go.mod` and `go.sum`: what they pin and what they prove.  Import paths as
URLs, version tags as the only release mechanism, and minimal version
selection, which picks the oldest version that will do.  The `go` and
`toolchain` lines.  Why `tailnet` is a module of its own inside this
repository, with its own tags, and what it kept out of everybody else's
build.  Then the working loop for a change that spans Gizmo and a server:
`replace` directives, `go.work` workspaces, and when each is the right
tool.  What counts as a breaking change to a Go API, what an exported name
commits you to, and what `internal/` lets you take back.  Tagging a
release and moving three consumers to it.  `v0`, `v1`, and the cost of
`v2`.

**Files to look at:** `go.mod`, `tailnet/go.mod`, `DESIGN.md` (Packages);
the `go.mod` of each of TaskMaster, Jiggle and Juke

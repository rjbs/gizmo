# Gizmo: design notes

Gizmo is a Go library for writing JMAP ([RFC 8620][rfc]) servers for new
data types.  It is being extracted from two working servers and is meant to
carry a third:

- **[Juke](https://github.com/rjbs/juke):** an MP3 library server with its
  own object types.
- **[TaskMaster](https://github.com/rjbs/taskmaster):** a server for the
  JMAP for Tasks draft.
- **Jiggle:** a photo gallery, to be written on Gizmo from the start.

These notes were drafted in November 2026 before any code existed.  They
record decisions, not implementation.  When the code and these notes
disagree, fix one of them.

[rfc]: https://www.rfc-editor.org/rfc/rfc8620

## Shape

Gizmo holds nothing that belongs to the application.  It does not own the
process, the HTTP mux, the database handle, or a transaction.  The
application opens its databases, mounts Gizmo's handlers on its own mux
beside whatever else it serves, and starts its own background work.

Gizmo does call back into the application: a data type is described by a
set of hooks, and Gizmo's drivers call them.  It also prescribes
conventions, such as the tables that track state.  The line is that Gizmo
may ask the application to follow a convention, but never takes a resource
away from it.

The reason is that in all three servers the database is the application's,
and JMAP is one of several things that write to it.  Juke has import and
backfill commands.  Jiggle has background workers that finish an import
long after the request that started it.

## Standard JMAP, exactly

The standard methods behave as RFC 8620 says.  When a consumer wants a
standard method to behave differently, the answer is to change the
consumer, not to add a mode to Gizmo.  A coherent CRUD model that works the
same everywhere is the thing JMAP provides, and is worth more than any one
convenience.

- **`/set` is per object.**  Each create, update and destroy succeeds or
  fails alone.  There is no all-or-nothing mode.
- **`/set` runs destroy, then create, then update.**  Destroying first lets
  a value freed by a destroy be taken by a create or an update in the same
  call.  Creating comes before updating because RFC 8620 Â§5.3 requires a
  creation to happen before anything in the call that refers to it, which
  an update may.  A destroy that names an object made in the same call
  waits until it has been made.
- **Extensions go in custom methods,** registered by the application under
  its own capability.

Not built until a consumer needs them: `/queryChanges`, `/copy`,
`Blob/copy` and PushSubscription.

## Packages

The names are provisional.

- **`jmap`:** the request and response envelope, errors, the method
  registry and dispatch, result references, JSON Pointer, PatchObject,
  creation ids and capabilities.  No storage and no HTTP.
- **`std`:** drivers for `/get`, `/changes`, `/set` and `/query`, written
  against per-type hooks.  The driver owns argument handling, paging,
  `ifInState` and the shape of the response.  The type owns loading,
  validation, writing, and turning a filter and sort into results.
- **`httpd`:** the session resource, API endpoint, event source, upload and
  download, as handlers for the application to mount.
- **`blob`:** the interface an application implements to store and serve
  blobs.
- **`push`:** the broker that carries state changes to event sources.
- **`auth`:** the caller's identity in the request context, bearer-token
  and loopback middleware, and routing of an `accountId` to the
  application's backend for that account.
- **`sqlitekit`:** the SQLite conventions below: migrations, states,
  modseqs, tombstones, purging and id generation.  Optional; `std` does not
  depend on it.
- **`jmaptest`:** the HTTP fixture and assertion helpers for testing a
  server end to end.
- **`client`:** a JMAP client for programs that aren't tests: the session,
  requests of one call or several, uploads and downloads.  What a server
  refuses comes back as an error.  It has no event source yet.
- **`tailnet`:** a server's own node on a Tailscale network, its listener
  there, and Tailscale identity as an `auth` source.  It is a module of
  its own, `github.com/rjbs/gizmo/tailnet`, with its own version tags
  (`tailnet/v0.1.0`), so that Tailscale's dependencies and the version of
  Go it wants stay away from servers that don't want them.

## Requests

- **Methods are registered by name and capability.**  A method whose
  capability isn't in the request's `using` is an unknown method.
- **Arguments stay raw JSON until the method decodes them,** so a closed
  type can decode into a struct and an open one into a map.
- **A method call may produce more than one response.**  Nothing needs
  this yet, but `/copy` will, and allowing it later would change the
  signature of every method.
- **Creation ids are kept for the whole request** and resolved by Gizmo, not
  by each method.  Any method that creates objects, standard or custom, can
  record them, so that a later call in the request can refer to them.
- **Every request has an id,** and with the caller's identity it is visible
  to every hook and custom method.  Jiggle uses the two to stamp its change
  log.
- **Limits that are advertised are enforced,** and upload concurrency is
  configurable.  A user's request or upload past the number allowed at
  once waits its turn; it is not refused.

## Custom methods

`std` is a kit of parts, not only four closed drivers.  A custom method
that creates or changes objects, such as Jiggle's `Photo/import`, gets
everything `/set` gets: the write transaction, a state bump for the types
it touches, per-item success and failure, the request id and the caller's
identity, and a place to record creation ids.

## Transactions

- **One write transaction per method call,** not per request.  JMAP makes
  no promise that a request is atomic, and a transaction per request would
  hold the write lock across unrelated calls.
- **One savepoint per object** inside it.  An object's failure rolls back
  that object alone, including anything its hooks did to other objects.
- **An object's change may change other objects, of any type,** inside its
  savepoint.  Destroying a Jiggle Photo takes it out of its Albums.  The
  transaction tracks every type touched, so each one's state moves and is
  announced.
- **The application begins the transaction.**  Writers that are not JMAP
  requests use the same kit with a transaction of their own.

## State

This is `sqlitekit`'s convention.  It follows
[Ix](https://github.com/fastmail/Ix), an earlier Perl JMAP framework, with
an epoch added.

- **A states table** holds one row per type, with `lowest_modseq` and
  `highest_modseq`.
- **Each object changed takes a new modseq** from its type's row.  Objects
  record the modseq of their creation and of their last change.
- **The state string** is an epoch and the type's `highest_modseq`.  The
  epoch is a random token made when the database is created, so a client's
  state from before a database was rebuilt is recognized as foreign.
- **Restoring a backup calls for a new epoch.**  A restored database keeps
  its epoch and has gone back in time.  Once its modseqs pass the ones
  clients hold, those clients would be given wrong changes and no error.
  `sqlitekit` can give a database a new epoch, and whatever restores one
  should ask it to.
- **Tombstones live in their own table.**  A destroy deletes the object's
  row and records a tombstone, so tables of live objects hold only live
  objects.
- **Creating an id that has a tombstone removes the tombstone.**  The
  object's creation modseq is the new one.  Only a writer that supplies its
  own id can do this; Jiggle's undo of a destroy is the case in mind.
- **Tombstones are purged by age.**  The application calls the purge, with
  its own transaction.  Purging raises `lowest_modseq` and does not change
  the state.
- **`/changes` refuses with `cannotCalculateChanges`** when the client's
  state is from another epoch or is below `lowest_modseq`.
- **A paged `/changes` may call an object updated that the client was
  never told of.**  When `maxChanges` cuts a response short, its
  `newState` is a modseq partway along, and the next page judges what is
  new against that.  An object made before that point and changed after
  it was not on the first page, where its newer modseq put it later, and
  is on a later page as updated.  A client must treat an updated id it
  doesn't hold as a creation.  Cyrus and Ix behave the same way, so a
  client that works with them already does.  The cure would be states
  that remember where paging began, which was judged not worth a second
  form of state string.

## Accounts

One SQLite database per account.  JMAP suits this: every method call names
one account, and even `/copy` is designed not to need a transaction across
two.

- **No table has an `accountId` column.**  The database is the account.
- **Isolation is by construction.**  A query cannot reach another
  account's data by forgetting a condition.
- **`jmap` and `std` don't know this.**  They ask the application to
  resolve an `accountId`; only `sqlitekit` assumes a database is an
  account.
- **An account the caller may not use looks like one that doesn't exist.**
  Both are `accountNotFound`, so a caller can't learn which accounts
  exist.  A call that names no account is `invalidArguments`.
- **What belongs to a user and not an account,** such as credentials and
  the list of a user's accounts, lives outside any account's database.

## Ids

- **Ids are opaque strings.**  Gizmo never reads meaning out of an id and
  never treats one as a number.  The only reserved shape is a leading `#`,
  which marks a creation reference.
- **Ids the RFC advises against must still work.**  Jiggle has thousands of
  published ids that begin with a digit, and some that are all digits.
- **`sqlitekit` generates ids.**  A type may supply its own generator; one
  that doesn't gets a default.  Uniqueness comes from the table's
  constraint, with a retry on collision.

## Browsers

A server that decides who a request is from by where it comes from, such
as the loopback interface or a tailnet node, can be reached by any page
open in a browser there.  The HTTP server guards against that, with no
configuration needed to turn it on.

- **Cross-origin writes are refused.**  A request that could change
  something, and that a browser sent for a page from another origin, gets
  a 403.  An origin can be named as trusted, for a web client served from
  elsewhere.
- **Requests to a name the server isn't known by are refused,** with a 421.
  This is against DNS rebinding, which would let a page read through the
  API.  IP addresses and `localhost` always pass.  The application gives
  the server's other names: its base URL, and its name on a tailnet.
- **Clients that aren't browsers are unaffected** by the first guard, and
  by the second so long as they use one of the server's names.
- **What the application serves beside JMAP** it guards with the same
  guard, which it can ask the server for.

## Push

- **The event source is fed by a broker** that anything may publish to.  A
  committed transaction publishes the states it changed; so can a background
  worker.
- **Writers in other processes** can't publish, so a server that has them
  also polls its states tables.

## Blobs

The application decides what a blobId is and where the bytes live.  Gizmo
defines the interface between that and the upload and download endpoints.

- **Uploads stream.**  An upload is never held whole in memory; Jiggle
  expects files of several gigabytes.
- **Downloads support range requests.**
- **Whether an upload is still wanted is the application's answer,** and
  so is removing blobs, including uploads nothing ever claimed.
- **The upload path stays open to chunked or resumable upload,** which
  Jiggle may need.

## Testing

Servers built on Gizmo are tested end to end over HTTP, with `jmaptest`.
Gizmo's own suite does the same against a small server kept for the
purpose.

`jmaptest` is a JMAP client built for tests, after
[JMAP::Tester](https://github.com/fastmail/JMAP-Tester), and keeps its
vocabulary.  A *sentence* is one method response, and a *paragraph* is
the run of sentences answering one method call.  Asking for the single
sentence of a response, or the sentence with some name, fails the test if
the response isn't shaped that way.  The sentence for a `/set` can give
the id made for a creation id, and can fail the test, showing the errors,
if anything was not created, updated or destroyed.

Juke and TaskMaster move to Gizmo before Jiggle is written, so some of what
Jiggle needs will have no real consumer for a while.  Gizmo's own suite
covers those, so that they aren't designed blind:

- **Large streaming uploads.**
- **State changes and push from a writer outside any request.**
- **A custom method that creates objects,** with per-item results and
  creation ids used later in the request.
- **Recreating a destroyed id.**
- **The application deciding whether an upload is still wanted.**
- **Awkward ids:** ones that begin with a digit, are all digits, begin with
  zero, or read as a number in exponent notation, such as `123e456789`.

## Plan of work

1. **Core and HTTP.**  `jmap`, `auth`, `push`, `httpd` and `jmaptest`, with
   TaskMaster and Juke ported onto them.
2. **Standard methods.**  `std`, starting from Juke's drivers, with
   TaskMaster's hand-written `/get` and `/set` ported onto it.
3. **State.**  `sqlitekit`, with both servers' databases migrated to its
   convention.
4. **Blobs.**  The interface, with Juke's downloads and artwork uploads
   ported onto it.
5. **Jiggle.**

Nothing built on Gizmo has a user beyond its author, so a cleaner design
is preferred to compatibility with existing ids, state strings or schemas.
The exception is Jiggle's existing photo ids, which are in published URLs.

## Undecided

- **The default id format.**  Proposed: sixteen characters of lowercase
  base32, the first always a letter.  Whether it should carry a one-letter
  type prefix, as Juke's ids do, is open.
- **A `/changes` state above the current one.**  Ix answers
  `invalidArguments`; Juke and TaskMaster answer `cannotCalculateChanges`.
  Proposed: `cannotCalculateChanges`, because the likely cause is a
  restored database, and the client's remedy is to fetch everything
  again.  `invalidArguments` would be kept for a state that is missing or
  isn't a state string at all.
- **Tombstones in their own table, or in the object's row.**  A separate
  table for now; this may be revisited.

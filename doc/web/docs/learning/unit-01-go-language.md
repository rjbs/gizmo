# 1. Go as a language

*Written against gizmo `f549a2d`.*

## Introduction

Go is a small language on purpose.  Its [specification](https://go.dev/ref/spec)
is about a hundred pages and can be read in an afternoon, which is not a thing
anybody says about Perl or Rust.  Most of what you know from Rust has a plainer
cousin here, and a good deal of it has no cousin at all: there are no sum
types, no `Option`, no borrow checker, no macros, and no expressions where a
statement will do.  What you get in return is code that looks the same
everywhere, compiles in a second or two, and can mostly be read without
knowing what came before it.

This unit covers the language as far as it takes to read three of the plainest
files in Gizmo: `jmap/pointer.go`, which resolves a JSON Pointer;
`jmap/patch.go`, which applies a PatchObject; and `jmap/limits.go`, which is a
struct and one method.  You know what all three are *for*, so the only new
thing on the page is Go.

Structs, methods, pointers, and errors all appear here, because no Go file
lacks them, but only as much as these files need.  Unit 2 is about them.

---

## Packages, files, and imports

A Go [package](https://go.dev/ref/spec#Packages) is a directory.  Every `.go`
file in `jmap/` begins with `package jmap`, and together they are one package
with one namespace.  `patch.go` calls `UnescapeToken`, which is defined in
`pointer.go`, with no import and no qualification, because they are the same
package.  The division into files is for the reader and means nothing to the
compiler.

This is neither Perl's arrangement nor Rust's.  In Perl, a file is
conventionally a package, and `package` can appear anywhere.  In Rust, a file
is a module, and the tree of modules is declared with `mod`.  In Go, there is
no declaration: the directory is the unit, and a subdirectory is an unrelated
package that happens to live nearby.  `jmap` has no special access to anything
in a `jmap/foo`, nor the reverse.

A package is named in an [import](https://go.dev/ref/spec#Import_declarations)
by its path, which is the module's path (from `go.mod`,
`github.com/rjbs/gizmo`) plus the directory:

```go
import (
	"encoding/json"

	"github.com/rjbs/gizmo/jmap"
)
```

After that, the package is referred to by the last element of its path:
`json.Marshal`, `jmap.Limits`.  Paths with no dot in the first element
(`fmt`, `strings`, `encoding/json`) are the standard library.  Everything
imported is always written with its package name in front, so in any Go file
you can tell where a name comes from: `strings.Split` is from the import,
`UnescapeToken` is from this package, and there is no third possibility.  (No
`use Foo qw(:all)`, no glob imports to speak of.)

Three rules that will bite early:

- **An unused import is a compile error**, not a warning.  So is an unused
  local variable.  Go has no warnings at all; things are errors or they are
  the business of `go vet`.
- **Imports can't be circular.**  If `std` imports `jmap`, then `jmap` cannot
  import `std`, even indirectly.  This shapes package design a great deal, and
  is why Gizmo's dependency order is so clean.
- **One file holds the package's documentation.**  A comment directly above
  the `package` clause is the package's doc comment; for `jmap` it is in
  `invocation.go`.

---

## Names and visibility

There is no `pub`.  A name that begins with a capital letter is
[exported](https://go.dev/ref/spec#Exported_identifiers) from its package, and
any other name is not:

```go
func UnescapeToken(s string) string        // usable as jmap.UnescapeToken
func resolveTokens(v any, tokens []string) // usable only inside package jmap
```

That is the entire access control system.  It applies to functions, types,
constants, variables, struct fields, and methods alike.  The boundary is always
the package: there is nothing like "private to this type" or "private to this
file".  Every file in `jmap/` can see every unexported name in `jmap/`.

The consequence for reading is pleasant.  The case of the first letter tells
you, at every use, whether you are looking at API or at plumbing.

The consequence for naming is that callers always see the package name, so
names don't repeat it.  The type is `jmap.Limits`, not `jmap.JMAPLimits`; the
constructor in `push` is `push.NewBroker`.  Other conventions that the code
follows: names are `mixedCaps`, never `snake_case`; initialisms keep their
case, so it is `AccountID` and `baseURL`, not `AccountId`; and short-lived
variables get short names (`v`, `tok`, `i`, `err`).  That last one feels wrong
for a week.

---

## Declarations

The type comes after the name.  The long form of a variable
[declaration](https://go.dev/ref/spec#Variable_declarations) is rare inside
functions, where the
[short form](https://go.dev/ref/spec#Short_variable_declarations) `:=` declares
and infers at once:

```go
var count int               // declared, with its zero value
var name string = "kitchen" // declared with a value; the type is redundant
tok := UnescapeToken(raw)   // declared, type inferred: the usual form
```

`:=` works only inside a function.  At package level you write `var`, `const`,
`type`, or `func`.

`=` assigns to a variable that already exists; `:=` makes a new one.  With
several names on the left, `:=` needs only *one* of them to be new, and
assigns to the rest.  `patch.go` uses this at lines 37 and 41:

```go
child, ok := parent[t]            // declares child and ok
...
m, ok := child.(map[string]any)   // declares m, assigns to the same ok
```

The trap is that `:=` in an inner block always declares, and so shadows.
Go has no `let` to make shadowing look deliberate, so an accidental `err :=`
inside an `if` leaves the outer `err` untouched and nothing complains unless
the new one goes unused.

A name you must receive but don't want is given to the
[blank identifier](https://go.dev/ref/spec#Blank_identifier), `_`:

```go
for _, el := range cur {
```

There is no immutability for variables.  Nothing corresponds to `let` against
`let mut`.  [`const`](https://go.dev/ref/spec#Constant_declarations) exists,
but only for numbers, strings, and booleans known at compile time, as in the
block of error type names in `jmap/errors.go`:

```go
const (
	SetErrForbidden    = "forbidden"
	SetErrInvalidPatch = "invalidPatch"
	...
)
```

These are how Go spells what Rust would make an enum.  There are no enums.
The nearest thing is a named integer type and a run of constants numbered by
[`iota`](https://go.dev/ref/spec#Iota), which Jiggle uses for its worker lanes
and which we'll meet there.  The compiler doesn't check that a `switch` over
them covers every case.

---

## Zero values

Every type has a [zero value](https://go.dev/ref/spec#The_zero_value), and
every variable starts out holding it.  There is no uninitialized memory and no
`undef`:

| Type | Zero value |
|---|---|
| numbers | `0` |
| `string` | `""` |
| `bool` | `false` |
| pointers, slices, maps, functions, interfaces, channels | `nil` |
| a struct | every field at its own zero value |

This is as if every Rust type implemented `Default` and the compiler filled in
`..Default::default()` for you.  Go code is designed around it.  A struct
whose zero value is useful needs no constructor, and "make the zero value
useful" is a proverb.

`Limits` is the example at hand.  A caller writes only the fields it cares
about:

```go
k := newKitchen(t, jmap.Limits{MaxObjectsInGet: 4})   // std/std_test.go:127
```

and the others are zero, which `WithDefaults` reads as "not set".  The cost of
the convention is in the comment on line 26 of `limits.go`: a zero can't be
told from an absence.  `MaxSizeUpload` has no default *because* zero has to be
able to mean zero there.  Where the difference matters, Go code reaches for a
pointer, which can be `nil`; you'll see `*int` and `*bool` in the request
structs in Unit 3 for exactly this reason.

---

## The basic types

The [numeric types](https://go.dev/ref/spec#Numeric_types) are what you'd
expect: `int8` through `int64`, the unsigned ones, `float32`, and `float64`.
Then there is `int`, which is the size of a machine word (64 bits anywhere
you'll run this) and is the type of every length and index.  `byte` is another
name for `uint8`, and `rune` is another name for `int32` and holds a Unicode
code point.

**There are no implicit numeric conversions**, not even widening ones.
`Limits` has both `int` and `int64` fields, and you can't assign one to the
other without writing `int64(n)`.  This is stricter than Rust only in that
there's no `.into()`; the conversion is a cast every time.

A [`string`](https://go.dev/ref/spec#String_types) is an immutable sequence of
*bytes*, which by convention and nearly always in fact holds UTF-8.
[`len`](https://pkg.go.dev/builtin#len) counts bytes, and indexing and slicing
work on bytes:

```go
return resolveTokens(v, strings.Split(pointer[1:], "/"))   // pointer.go:26
```

`pointer[1:]` is the string without its first byte.  That's safe here because
line 23 has just established that the first byte is `/`.  Unlike Rust, slicing
through the middle of a multi-byte character isn't a panic: you just get a
string that isn't valid UTF-8, and nobody tells you.  And unlike Perl, there's
no character-string and byte-string duality to keep straight, because there
are only bytes.  When you want characters, you `range` over the string, which
yields runes, or convert to `[]rune`.

Strings are concatenated with `+` and compared with `==`.  There's no
interpolation; you build strings with `+` or with
[`fmt.Sprintf`](https://pkg.go.dev/fmt#Sprintf).  Raw strings, with no escapes,
are written in backquotes, which is why the tests are full of
`` `{"a": 1}` ``.

---

## Functions

A [function](https://go.dev/ref/spec#Function_declarations) lists its
parameters with types after names, then its results:

```go
func UnescapeToken(s string) string
func ResolvePointer(v any, pointer string) (any, error)
func ApplyPatch(obj map[string]any, patch map[string]any) *SetError
```

Functions return **multiple values**.  This isn't a tuple, which Go doesn't
have: the results can only be assigned to that many variables, at once.
By iron convention, a function that can fail returns its error last:

```go
idx, err := strconv.Atoi(tok)
if err != nil || idx < 0 || idx >= len(cur) {
	return nil, fmt.Errorf("bad array index %q", tok)
}
```

That is `Result<T, E>` taken apart into two variables with nothing to make you
look at the second one, apart from the unused-variable rule.  And this, which
you'll read ten thousand times, is `?` written out:

```go
r, err := resolveTokens(el, tokens[i+1:])
if err != nil {
	return nil, err
}
```

Unit 2 takes errors seriously.  For now: `error` is an ordinary type, `nil`
means success, and when the error is non-nil the other results are
conventionally their zero values and not to be used.

What functions lack: default arguments, named arguments, and overloading.
A function has one signature.  What they have: a
[variadic](https://go.dev/ref/spec#Passing_arguments_to_..._parameters) final
parameter, written `args ...any`, and the matching `xs...` at a call to spread
a slice into it.  Both forms are in `pointer.go`:

```go
fmt.Errorf("no property %q", tok)   // tok goes to a ...any parameter
out = append(out, arr...)           // arr is spread into one
```

**Arguments are always passed by value.**  Everything is copied: a struct
argument is a copy of the struct.  This sounds like it contradicts the comment
on `ApplyPatch`, which says it changes `obj` "in place", and the resolution
matters enough to get its own two sections below.  A slice or a map *is* a
small value that refers to storage elsewhere, and it's that small value which
gets copied.

---

## Control flow

There is little of it, and no parentheses around conditions.

[`if`](https://go.dev/ref/spec#If_statements) may begin with a statement,
whose variables are scoped to the `if` and its `else`:

```go
if arr, ok := r.([]any); ok {     // pointer.go:47
	out = append(out, arr...)
} else {
	out = append(out, r)
}
```

This is the nearest Go comes to `if let`, and it's used the same way.  `if` is
a statement, not an expression, and there's no ternary operator.  You declare
the variable, then assign to it in each branch.

[`for`](https://go.dev/ref/spec#For_statements) is the only loop.  It has a C
form (`for i := 0; i < n; i++`), a `while` form (`for cond`), an infinite form
(`for {`), and `range`, which is the common one:

```go
for i, raw := range tokens { ... }   // a slice: index and element
for k, val := range patch { ... }    // a map: key and value
for k := range patch { ... }         // a map: just the keys
for i := range tokens { ... }        // a slice: just the indexes
for i := range len(rest) { ... }     // an integer: 0 up to it (auth/token.go:46)
```

Ranging over a string gives byte offsets and runes.  Ranging over a function
is how Go spells a custom iterator, and waits for Unit 7.

The element variable is a *copy*.  Assigning to `raw` above would change
nothing in `tokens`, which is why `patch.go` line 30 unescapes its tokens by
index:

```go
for i := range tokens {
	tokens[i] = UnescapeToken(tokens[i])
```

[`switch`](https://go.dev/ref/spec#Switch_statements) needs no `break`: a case
doesn't fall through.  Cases may be any expressions, not only constants.  A
`switch` with nothing after the keyword takes boolean cases and is the tidy
way to write an `if`/`else if` chain; `checkPatch` in `jmap/patch_test.go` is
one.  No `switch` is checked for exhaustiveness, so a `default` is your job.

There are no exceptions, and so no `try`.  There is `panic`, for bugs, and
Unit 2 says when it's used.

---

## Slices

A [slice](https://go.dev/ref/spec#Slice_types), `[]T`, is the workhorse
sequence.  It's a three-word value: a pointer into an array, a length, and a
capacity, the capacity being how far the array runs past the pointer.  Go also
has fixed-size [arrays](https://go.dev/ref/spec#Array_types), `[4]int`, but
you'll rarely see one; they exist to be the storage under slices.

In Rust terms, a slice is a `&mut [T]` that will also grow like a `Vec<T>`
when it can, in a language with no borrow checker to notice when two of them
overlap.  Most of what's subtle about slices follows from that sentence.

You make one with a literal, or with
[`make`](https://pkg.go.dev/builtin#make), which takes a length and optionally
a capacity:

```go
out := []any{}                           // pointer.go:41: empty
keys := make([]string, 0, len(patch))    // patch.go:12: empty, room for all
```

and you grow one with [`append`](https://pkg.go.dev/builtin#append), which
returns a slice that you *must* assign back:

```go
keys = append(keys, k)
```

`append` writes into the spare capacity if there's any, and otherwise
allocates a larger array, copies, and returns a slice of that.  Either way the
length is new, and the length lives in the slice value, of which the function
got a copy.  Hence the assignment.  (`make([]string, len(patch))`, with one
number, would give a slice already holding that many empty strings, and the
appends would land after them.  It's a classic slip.)

The slicing expression `s[lo:hi]` makes a new slice value over the *same*
array.  Nothing is copied:

```go
for _, t := range tokens[:len(tokens)-1] {       // patch.go:36: all but the last
r, err := resolveTokens(el, tokens[i+1:])        // pointer.go:43: the rest
```

`tokens[i+1:]` is legal when `i` is the last index: it's an empty slice, and
the recursive call's loop runs zero times.  An index past the length, on the
other hand, panics, as in Rust.

Shared storage is cheap and it is also the trap:

```go
t := []string{"a", "b", "c"}
u := t[:2]          // u is [a b], with room for one more
u[0] = "Z"          // t is now [Z b c]
u = append(u, "Q")  // room was there, so this wrote over t[2]
// t is [Z b Q]
```

Nothing in Gizmo's three files does this, because they only read through
their sub-slices.  The working rules are: assign the result of `append` back
to the slice you appended to; don't keep using an old slice after appending to
another view of it; and when you need an independent copy, ask for one with
[`slices.Clone`](https://pkg.go.dev/slices#Clone).

### nil slices and empty slices

The zero value of a slice is `nil`: no array, length zero.  A nil slice is a
perfectly good empty slice.  You can take its `len`, `range` over it, and
`append` to it, so `var out []any` and `out := []any{}` nearly always behave
alike.

Nearly.  `encoding/json` encodes a nil slice as `null` and an empty one as
`[]`.  In a JMAP server that difference goes out on the wire, and it's why
`pointer.go` line 41 is written the way it is: `/list/*/id` over an empty list
should give `[]`.  Watch for this whenever a slice is headed for a response.
Exercise 4 has you break it.

---

## Maps

A [map](https://go.dev/ref/spec#Map_types), `map[K]V`, is a hash.  The key
type has to be comparable with `==`, which rules out slices and maps as keys.

```go
m := map[string]any{}     // empty, ready for use
m["a"] = 1
delete(m, "a")            // patch.go:49; deleting an absent key is fine
```

Indexing a map with a key it doesn't have is not an error.  You get the zero
value of `V`.  To tell absent from zero there's a second, optional result, the
"comma ok" form, which you'll see everywhere:

```go
next, ok := cur[tok]      // pointer.go:34
if !ok {
	return nil, fmt.Errorf("no property %q", tok)
}
```

So there's no `exists`, and no `Option`: a Go API that might have no answer
returns `(value, bool)` or `(value, error)`.

Three more things about maps:

- **A map value is a reference** to a hash table kept by the runtime.
  Passing a map to a function copies the reference.  That's the resolution
  promised above: `ApplyPatch` receives a copy of `obj`, the copy refers to
  the caller's table, and `parent[last] = val` on line 51 changes the
  caller's object.  Line 45, `parent = m`, walks down into a nested object
  the same way, without copying anything.
- **The zero value is a nil map**, which reads as empty but *panics* if you
  assign into it.  Unlike a nil slice, a nil map isn't ready for use, and
  there's no autovivification to save you.  Maps are made with a literal or
  with `make`.
- **Iteration order is random**, and deliberately randomized further so that
  no one comes to rely on it.  `ApplyPatch` ranges over `patch` twice and
  doesn't care, because once the prefix check has passed, no two paths can
  touch the same property.  Where order shows, as in an error message that
  names the first unknown argument, the code sorts the keys first
  (`sortedKeys`, `std/std.go:159`).

There's no set type.  A set is a `map[T]bool` or a `map[T]struct{}`; `dedupe`
at `std/std.go:168` keeps one called `seen`.

---

## `any`, type assertions, and type switches

[`any`](https://pkg.go.dev/builtin#any) is a type that can hold a value of
every type.  It's an [interface](https://go.dev/ref/spec#Interface_types) with
no methods, and before Go 1.18 was spelled `interface{}`, which you'll still
see in older code and in some compiler messages.  An `any` holds a value and
remembers the value's type, so it can be asked about at run time.

Rust models a JSON document as `serde_json::Value`, an enum.  Go has no enum
to do it with, so a decoded JSON document is an `any` that will turn out to
hold one of:

| JSON | Go |
|---|---|
| object | `map[string]any` |
| array | `[]any` |
| string | `string` |
| number | `float64`, or `json.Number` as Gizmo decodes |
| `true`, `false` | `bool` |
| `null` | `nil` |

That is the shape of every JMAP object as `jmap` sees it, and of `obj` and
`patch` in `ApplyPatch`.  It's about as close to a Perl data structure as Go
gets, and it has to be taken apart explicitly.

A [type assertion](https://go.dev/ref/spec#Type_assertions) asks for the value
as one type:

```go
m, ok := child.(map[string]any)     // patch.go:41
if !ok {
	return &SetError{..., Description: "path " + k + " passes through a non-object"}
}
```

With two results it reports failure in `ok`.  With one result,
`child.(map[string]any)`, it panics on failure.  Prefer two.

A [type switch](https://go.dev/ref/spec#Type_switches) asks about several
types at once, and is what a `match` on `serde_json::Value` becomes:

```go
switch cur := v.(type) {            // pointer.go:32
case map[string]any:
	next, ok := cur[tok]            // here cur is a map[string]any
	...
case []any:
	...
	v = cur[idx]                    // here cur is a []any
default:
	return nil, fmt.Errorf("cannot descend into %T with %q", v, tok)
}
```

Within each case, `cur` has that case's type.  Nothing checks that the cases
are exhaustive, so the `default` isn't decoration: without it, a pointer that
tried to descend into a string would fall out of the `switch` and carry on.

A JSON `null` arrives as a nil `any`, which is how line 48 of `patch.go`
recognizes a property to remove:

```go
if val == nil {
	delete(parent, last)
```

`any` is the escape hatch from a static type system, and idiomatic Go doesn't
use it where a real type will do.  Gizmo uses it for JSON whose shape isn't
known until run time, which is the honest use.

---

## Structs, and one method

A [struct](https://go.dev/ref/spec#Struct_types) is a record with named
fields:

```go
type Limits struct {
	MaxSizeUpload       int64
	MaxConcurrentUpload int
	...
}
```

`type Name ...` defines a new named type.  Field names follow the same
capitalization rule as everything else: these are all exported.

A struct value is written as a
[composite literal](https://go.dev/ref/spec#Composite_literals) that names the
fields it sets and leaves the rest at zero:

```go
jmap.Limits{MaxObjectsInGet: 4}
```

A struct is a value.  Assigning one, passing one or returning one copies every
field, as with a Rust type that is `Copy`.  There are no moves in Go: the
original remains usable after any of these.

A [method](https://go.dev/ref/spec#Method_declarations) is a function with a
*receiver* declared before its name:

```go
func (l Limits) WithDefaults() Limits {
	if l.MaxConcurrentUpload == 0 {
		l.MaxConcurrentUpload = 4
	}
	...
	return l
}
```

`l` is Go's `self`, and it's named whatever you like, by custom a letter or
two.  The receiver here is a `Limits`, not a pointer to one, so `l` is a copy,
and the assignments in the body change the copy.  That's why the method
returns `l`, and why its callers write:

```go
limits = limits.WithDefaults()    // std/get.go:65
```

Calling it without using the result does nothing at all.  The other kind of
receiver, `(l *Limits)`, gets a pointer and can change the original; the
difference is Rust's `self` against `&mut self`, without the compiler holding
you to it.  Unit 2 covers how to tell which a type wants.

Meanwhile, in `patch.go`, there are pointers to structs:

```go
return &SetError{
	Type:        SetErrInvalidPatch,
	Description: "patch path " + a + " is a prefix of " + b,
}
```

`&T{...}` makes a struct and gives its address, and `*SetError` is the type of
that address.  There's no `new`/`delete` ceremony and nothing to free.  Go is
garbage collected, and it's fine to return the address of something that looks
local.  `return nil` at the bottom of `ApplyPatch` returns the nil pointer,
meaning no error.

Two small matters of syntax show here.  The comma after the last field is
required when the literal spans lines.  And the opening brace of anything
must be on the same line as what it opens.  Both come from one rule: the
lexer [inserts semicolons](https://go.dev/ref/spec#Semicolons) at line ends,
and those are the places where one would land wrong.

---

## Printing and formatting

[`fmt`](https://pkg.go.dev/fmt) is `printf` with a few verbs worth learning
on sight, since they're all over the error messages:

| Verb | Meaning |
|---|---|
| `%v` | the value, in its default format; works on anything |
| `%+v`, `%#v` | the same with field names; as Go source |
| `%s`, `%d` | string; integer |
| `%q` | a string, quoted and escaped: the right verb for user input |
| `%T` | the value's *type* |
| `%w` | in `Errorf` only: wrap this error (Unit 2) |

`pointer.go` line 61 uses `%T` to say what it found where it wanted an object
or an array.  `%#v` is the nearest thing to `Data::Dumper` or `{:?}`, and it
earns its place in debugging.

[`fmt.Errorf`](https://pkg.go.dev/fmt#Errorf) formats a message and returns it
as an `error`.  It's the plainest way to make one.

---

## What isn't here

A list, so that you can stop looking for these.

Coming from Rust:

- **Sum types, `Option`, `Result`, pattern matching.**  In their places:
  constants, `nil` and the zero value, a second return value, and the type
  switch.
- **Ownership, borrowing, lifetimes.**  The garbage collector owns
  everything.  Aliasing is unrestricted, and so mutation through an alias is
  yours to keep track of.
- **Immutability.**  No `let` against `let mut`, no `&` against `&mut`.  If
  you can reach it, you can change it.
- **Traits with `impl` blocks.**  Go has interfaces, satisfied implicitly
  (Unit 6).
- **Iterator chains and closures-as-style.**  Go has closures, and since
  recently has iterators, but the idiom is a `for` loop with the work written
  out.  `ApplyPatch` collects a map's keys in three lines where Rust would
  have `.keys().collect()`.
- **Macros and derive.**  Go has code generation as a separate step, which
  none of these projects uses, and run-time reflection, which `encoding/json`
  uses heavily (Unit 3).

Coming from Perl:

- **Context.**  A function returns what its signature says, every time.
- **Autovivification.**  Assigning into a nil map panics, and intermediate
  levels are never made for you; `ApplyPatch` refuses a path whose parent
  doesn't exist, where a Perl hash would have grown one.
- **Regex and interpolation as syntax.**  Both are library calls:
  [`regexp`](https://pkg.go.dev/regexp) and `fmt`.
- **More than one way to do it.**  Usually there's one, and `gofmt` has
  already decided how it's indented.

---

## The toolchain

Everything is a subcommand of [`go`](https://pkg.go.dev/cmd/go).  The pattern
`./...` means "this directory and everything under it".

| Command | What it does |
|---|---|
| `go build ./...` | compiles everything, and for a library, throws the result away: a fast "does it compile?" |
| `go test ./...` | runs every package's tests |
| `go test ./jmap` | runs one package's |
| `go test ./jmap -run TestApplyPatch -v` | one test, verbosely |
| `go vet ./...` | reports suspicious code that compiles anyway |
| `gofmt -l .` | lists files that aren't formatted; `gofmt -w` rewrites them |
| `go doc ./jmap` | prints a package's documentation |
| `go doc ./jmap ApplyPatch` | prints one symbol's |
| `go doc strings.Cut` | the same, for the standard library |
| `go run .` | compiles and runs the `main` package in this directory |

Three notes.  `go test` caches: a package whose inputs haven't changed prints
`(cached)` and isn't run again, and `-count=1` forces it.  `gofmt` is not
optional or configurable, and it indents with tabs; every editor's Go support
runs it on save, and nobody discusses formatting.  And `go doc` shows what the
[doc comments](https://go.dev/doc/comment) say, which is why every exported
name in Gizmo has a comment that begins with the name itself: "ApplyPatch
applies…", "Limits are…".  That's the convention, and tools lean on it.

---

## Reading

- [A Tour of Go](https://go.dev/tour/) is the official interactive
  introduction.  With Rust behind you, it's an hour or two, and worth it for
  the sections on slices and maps
- [The Go Programming Language Specification](https://go.dev/ref/spec) is
  short and readable enough to be a reference you'll actually use
- [Effective Go](https://go.dev/doc/effective_go) predates generics and
  modules and says so, but remains the best account of the idioms
- [Go Slices: usage and internals](https://go.dev/blog/slices-intro) covers
  the pointer, length, and capacity model, with pictures
- [Strings, bytes, runes, and characters in Go](https://go.dev/blog/strings)
- [Go maps in action](https://go.dev/blog/maps)
- [Go Doc Comments](https://go.dev/doc/comment) gives the conventions that
  `go doc` relies on
- [Go by Example](https://gobyexample.com/) has a short runnable program per
  feature, and is good for "how do I spell that again?"

---

## Code tour

### `jmap/limits.go`

Start here; it's the shortest.

**Lines 3–9: the doc comment.**  It begins with the name of the thing.  The
blank `//` line makes a paragraph break in `go doc`'s output.

**Lines 10–23: `Limits`.**  Sizes in bytes are `int64` and counts are `int`.
A comment directly above a field (lines 19–21) is that field's documentation.

**Lines 27–50: `WithDefaults`.**  A value receiver: `l` is a copy, modified
and returned.  `10_000_000` on line 32 is an integer literal with digit
separators.  Note what the method can't do: tell a caller who set
`MaxObjectsInGet` to zero from one who left it alone.

### `jmap/pointer.go`

**Lines 3–7: imports.**  All standard library.  A parenthesized `import`
block is the usual form, and `gofmt` sorts it.

**Lines 10–13: `UnescapeToken`.**  A parameter is an ordinary local variable,
so line 11 assigns to `s`.  The order of the two replacements matters, and is
the one RFC 6901 calls for: `~01` must become `~1`, not `/`.

**Lines 19–27: `ResolvePointer`.**  The exported entry point.  Two results,
`(any, error)`.  On the failing path the value is `nil`, and on the
succeeding one the error is.  `pointer[1:]` drops the leading `/` as a byte.

**Lines 29–65: `resolveTokens`.**  Unexported; the loop that does the work.
`v` is reassigned on each pass to walk down the document.

- **Line 32** is the type switch.  What decides between "key" and "index" is
  the type of the *value* at hand, never the look of the token.  That's what
  the test at `patch_test.go:85` is about: `"0"` and `"123456789012"` are
  keys when the thing being indexed is an object.
- **Lines 34–38** are the comma-ok lookup.
- **Lines 40–54** handle `*`.  It recurs once per element with the tokens
  that remain, flattens one level with `append(out, arr...)`, and *returns*,
  since the recursion has consumed the rest of the pointer.
- **Lines 55–59** convert the token and check bounds in one condition.
  Without the bounds check, line 59 would panic on a bad index, and a panic
  in a server is a much worse answer than an error.
- **Lines 60–61** are the `default`, for a pointer that tries to pass
  through a string, a number, or a null.

### `jmap/patch.go`

**Line 11: the signature.**  It returns `*SetError`, a concrete pointer type,
and not `error`.  That's unusual enough to be a decision, and Unit 2 explains
it.

**Lines 12–15** collect the keys of a map into a slice, with the capacity
known in advance.

**Lines 16–25** check every pair of keys for one being a path prefix of
another, which RFC 8620 forbids.  Quadratic, and patches are small.  Note
`a+"/"`: `"o"` is a prefix of `"o/x"` but not of `"other"`.

**Lines 27–53** apply each path.

- **Lines 28–34** split and unescape, writing back by index, and refuse an
  empty segment.
- **Lines 35–46** walk from the object down to the parent of the last
  segment.  `parent` is a map, which is to say a reference, so reassigning
  it on line 45 moves the reference and copies no data.  The two `ok`s on
  lines 37 and 41 are the same variable.
- **Lines 47–52** set or delete, by whether the JSON value was `null`.

**Line 54: `return nil`.**  No error.

Then read `jmap/patch_test.go` for what these functions are promised to do.
It's short, and it's the first look at how tests are written here: each case
is one line, a call to `checkPatch` or `checkPointer`.  Unit 5 is about
testing; for now it's enough to see that a test is a function named
`TestSomething` that takes a `*testing.T`.

---

## Exercises

Each of these leaves the tree as it found it, or says otherwise.  After any
of them, `git checkout jmap/` puts things back.

**1.  Read the documentation the way Go programmers do.**  Run
`go doc ./jmap`, then `go doc ./jmap ApplyPatch`, then `go doc strings.Cut`.
Notice that `resolveTokens` isn't in the package listing, and that
`go doc -u ./jmap resolveTokens` will show it anyway.

**2.  Break something and read the failure.**  In `jmap/patch.go`, change
line 48 from `if val == nil {` to `if false {`.  Run `go test ./jmap`.  Three
cases fail.  Before reading the output, guess which.  Then look at what a
failure tells you: the file and line of the *case*, its name, what was got,
and what was wanted.

**3.  Meet the compiler's strictness.**  In `UnescapeToken`, add a line
`x := 1` and run `go build ./jmap`.  Then remove it, and in `WithDefaults`
add `var n int = l.MaxSizeRequest`.  Read the errors from each.  Neither mistake would be more
than a lint in most languages.

**4.  nil against empty, on the wire.**  Predict what this evaluates to,
then add it to `TestResolvePointer` in `jmap/patch_test.go` and run the
tests:

```go
checkPointer(t, `{"list": []}`, "/list/*/id", `[]`)
```

Now change line 41 of `pointer.go` from `out := []any{}` to
`var out []any`, and run them again.  Before you added that case, the change
passed every test in the repository.  This case is worth keeping: it's a real
gap, and it belongs on `main`.

**5.  Slices share.**  Outside the repository, put this in a file called
`main.go` and run it with `go run main.go`.  Predict the output first.

```go
package main

import "fmt"

func main() {
	t := []string{"a", "b", "c"}
	u := t[:2]
	u[0] = "Z"
	u = append(u, "Q")
	fmt.Println(t, u, len(u), cap(u))

	u = append(u, "R")
	u[1] = "Y"
	fmt.Println(t, u, len(u), cap(u))
}
```

The second half is the other side of the trap.  Why does `u[1] = "Y"` not
show up in `t`?

**6.  A small edit, with the tests as a net.**  `ResolvePointer` checks for
a leading `/` on line 23 and then slices it off on line 26.
[`strings.CutPrefix`](https://pkg.go.dev/strings#CutPrefix) does both at
once, and `auth/token.go:42` shows it in use.  Rewrite `ResolvePointer` with
it, and run `go test ./jmap` and `gofmt -l .`.  Decide which version you'd
rather read.

**7.  Think about map order.**  `ApplyPatch` returns as soon as it finds a
bad path.  If a patch has *two* bad paths, each bad in a different way, which
`Description` does the caller get?  Is that a problem for a JMAP client?  For
a test?  Compare how `decode` in `std/std.go` (lines 124–126) arranges to always
complain about the same unknown argument.

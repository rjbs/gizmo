# Questions & answers

Questions that came up while reading the curriculum, with explanations.
These are folded back into the unit they came from before the next unit is
written.

---

## 1. Go as a language

**Does Go have an embedded documentation format, like Pod or JSDoc, with
tooling to build a web interface to it?**

Yes, though the format is much lighter than either.  A
[doc comment](https://go.dev/doc/comment) is just the run of `//` comments
directly above a top-level declaration, with no blank line between them.
There are no markers like `=head1` or `@param`: by convention the comment
is a sentence that begins with the name it documents, and parameters are
described in prose.  The comment above the `package` clause documents the
package, and begins "Package foo".  `auth/auth.go:1` is a long one, and
`auth/auth.go:114` documents `NewDirectory`.  A package with a lot to say
often puts its package comment in a file of its own, `doc.go`, holding
nothing else.

Since Go 1.19 the comments have a small markup, deliberately close to plain
text: a line beginning `# ` is a heading, an indented block is code, `-`
begins a list item, and `[Name]` or `[pkg.Name]` links to another
identifier.  `gofmt` normalizes it.  A paragraph beginning `Deprecated:`
is recognized by tools.

The tools:

- `go doc auth.NewDirectory` prints documentation in the terminal, much as
  `perldoc` does.
- [pkgsite](https://pkg.go.dev/golang.org/x/pkgsite/cmd/pkgsite), the
  program behind pkg.go.dev, serves the same HTML locally:
  `go run golang.org/x/pkgsite/cmd/pkgsite@latest` in a module's directory.
  The older `godoc` server still exists but is deprecated.
- pkg.go.dev itself builds pages for any public module on request, with no
  publishing step, as MetaCPAN does for anything uploaded to CPAN.

The nearest thing to Rust's doc tests is the
[example function](https://pkg.go.dev/testing#hdr-Examples): a function
named `ExampleNewDirectory` in a `_test.go` file is shown in
`NewDirectory`'s documentation, and if it ends with an `// Output:`
comment, `go test` runs it and checks what it printed.  Gizmo has none yet.

**A function's results must be assigned to exactly that many variables, so
`val := f()` is an error when `f` returns `(val, err)`.  But `for`/`range`
takes one variable or two, and so does reading from a map.  Is it only
functions that are strict?**

Yes.  A function call is always strict: `val := f()` fails to compile with
"assignment mismatch: 1 variable but f returns 2 values", and the way to
ignore a result is to name it `_`.  The callee has no say in this, and
there is no equivalent of Perl's `wantarray`: a function can't tell how many
of its results are wanted, and can't return a different number.

The flexible forms aren't functions.  They are a few pieces of syntax that
the language defines to have a one-value form and a two-value form, and the
compiler picks one by how many variables there are on the left:

- [Indexing a map](https://go.dev/ref/spec#Index_expressions): `v := m[k]`
  gives the value, or the zero value if `k` is absent; `v, ok := m[k]` adds
  whether it was there.  `internal/bakery/bakery.go:94` uses the second.
- [A type assertion](https://go.dev/ref/spec#Type_assertions): `f := x.(T)`
  panics if `x` isn't a `T`; `f, ok := x.(T)` doesn't, and says whether it
  was.  `httpd/eventsource.go:42` asks whether a `ResponseWriter` is also an
  `http.Flusher` this way.
- [A channel receive](https://go.dev/ref/spec#Receive_operator):
  `v, ok := <-ch` adds whether the channel is still open.  Channels come in
  a later unit.
- [`range`](https://go.dev/ref/spec#For_range) is part of the `for`
  statement, not an expression at all, and takes no variable, one, or two:
  `for k := range patch` at `jmap/patch.go:13`, and
  `for _, t := range tokens` at `jmap/patch.go:36`.

These are like those Perl built-ins whose parsing nothing written in Perl
can imitate: the language has them, and your own functions can't have the
same trick.  The idiom a function uses instead is to return the `ok` every
time, as `find` does at `internal/bakery/bakery.go:94`, and leave the caller
to write `a, ok := find(id)`.

The one place a multi-value call is accepted without being taken apart is
as the whole argument list of another call: `g(f())` works when `f`'s
results match `g`'s parameters exactly.

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

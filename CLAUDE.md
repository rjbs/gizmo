# Gizmo

## The Go learning booklet

`learning/`, on the `go-learning` branch, is a booklet that teaches Go by
way of Gizmo and the servers built on it.  `learning/docs/SYLLABUS.md` says how it
is organized, and `/next-unit` writes the next unit.

The site is built by Zensical from `learning/mkdocs.yml`:
`learning/build.sh` builds it in Docker, and `learning/build.sh serve`
previews it.  The theme and Markdown settings in `mkdocs.yml` are kept the
same as those of the Jinx manual (`../Jinx/doc/manual/mkdocs.yml`).

When the user asks a question about Go, or about this code as Go, during a
learning discussion, log it to `learning/docs/questions.md` under the heading of
the unit it came from, with the answer you gave, then commit it.  Do this
automatically without being prompted.

When writing unit content:

- The reader knows Perl and Rust well, and knows JMAP better than the text
  needs to explain.  Compare Go to Rust when it is more apt than a Perl
  comparison; use whichever language makes the concept clearest.
- The reader understands generics from Rust and from functional languages.
  Don't put them off or soften them.
- When mentioning a standard library package, type or function, or a
  language feature, for the first time, link to its official documentation:
  `pkg.go.dev` for the library, and the language specification or another
  page on `go.dev` for the language.
- Every example is real code from Gizmo, TaskMaster, jiggle or Juke where
  such code exists.  Cite it by file and line, and name the commits the unit
  was written against.

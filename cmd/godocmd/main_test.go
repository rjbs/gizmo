package main

import (
	"go/doc/comment"
	"testing"
)

// checkLink resolves a doc link, written in a comment on the page at from,
// to the package or symbol named by importPath, recv and name, as in
// [comment.DocLink], and compares the URL made against want.  The pages
// being written are those of a module example.com/m with packages auth,
// jmap and internal/bakery, and Gizmo's manual is given by -link.
func checkLink(t *testing.T, from, importPath, recv, name, want string) {
	t.Helper()

	pages := map[string]*page{}
	for _, rel := range []string{"auth", "jmap", "internal/bakery"} {
		pages["example.com/m/"+rel] = &page{rel: rel}
	}

	w := &writer{
		page:  pages["example.com/m/"+from],
		pages: pages,
		links: linkFlag{"github.com/rjbs/gizmo": "https://semiotic.systems/gizmo/manual/"},
	}

	link := &comment.DocLink{ImportPath: importPath, Recv: recv, Name: name}
	if got := w.linkURL(link); got != want {
		t.Errorf("link from %s to %q %q %q: got %q, want %q", from, importPath, recv, name, got, want)
	}
}

func TestLinkURL(t *testing.T) {
	checkLink(t, "auth", "", "", "Directory", "#Directory")
	checkLink(t, "auth", "", "Directory", "Lookup", "#Directory.Lookup")

	checkLink(t, "auth", "example.com/m/jmap", "", "", "jmap.md")
	checkLink(t, "auth", "example.com/m/jmap", "", "Call", "jmap.md#Call")
	checkLink(t, "auth", "example.com/m/jmap", "Call", "Arg", "jmap.md#Call.Arg")
	checkLink(t, "internal/bakery", "example.com/m/auth", "", "User", "../auth.md#User")
	checkLink(t, "auth", "example.com/m/internal/bakery", "", "", "internal/bakery.md")

	checkLink(t, "auth", "github.com/rjbs/gizmo/jmap", "", "Call",
		"https://semiotic.systems/gizmo/manual/jmap/#Call")
	checkLink(t, "auth", "github.com/rjbs/gizmo/internal/bakery", "", "",
		"https://semiotic.systems/gizmo/manual/internal/bakery/")

	checkLink(t, "auth", "net/http", "", "Handler", "https://pkg.go.dev/net/http#Handler")
	checkLink(t, "auth", "github.com/rjbs/gizmoid", "", "X", "https://pkg.go.dev/github.com/rjbs/gizmoid#X")
}

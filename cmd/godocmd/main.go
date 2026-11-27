// Command godocmd writes a module's package documentation as Markdown, one
// page per package and an index of them, for a Zensical site to build into
// its manual.  It is what pkg.go.dev shows, made a part of the project's own
// site, so that the manual can link to the rest of the site and the rest of
// the site to it.
//
// Usage:
//
//	godocmd -out DIR [-source URL] [-link PATH=URL ...] [packages]
//
// The packages are named as to go list, and are ./... if none are named.
// Commands are skipped, since they have no API to document.  Each package
// gets a page at its path within its module, so that the package
// github.com/rjbs/gizmo/auth is documented in DIR/auth.md, and DIR/index.md
// lists them all.  Anything else in DIR is left alone, but these pages are
// overwritten every time.
//
// Each declaration's heading gets the anchor pkg.go.dev would give it, like
// #NewDirectory or #Directory.Lookup, so that a link written for one is
// easily written for the other.  A doc link in a comment, like [Directory]
// or [jmap.Call], goes to the page and anchor for that name if its package
// is one being documented, to the URL given for it by -link if its import
// path begins with one given there, and otherwise to pkg.go.dev.  -link may
// be given more than once, and its URL is the base of that module's manual
// on its own site, so that a project built on Gizmo could give
//
//	-link github.com/rjbs/gizmo=https://semiotic.systems/gizmo/manual/
//
// If -source is given, each declaration links to its definition, at the
// URL made by appending the file's path within the module and #L and the
// line number to it.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/doc"
	"go/doc/comment"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// listedPackage is what this program needs from go list's description of a
// package.
type listedPackage struct {
	Dir        string
	ImportPath string
	Name       string
	GoFiles    []string
	CgoFiles   []string
	Module     *struct {
		Path string
		Dir  string
	}
}

// page is one package to document, and where its page goes.
type page struct {
	pkg  listedPackage
	rel  string // the page's path within the manual, without ".md"
	docs *doc.Package
	fset *token.FileSet

	// comments maps each source file's name to the comments in it, so that
	// a declaration can be printed with the comments inside it, like those
	// on a struct's fields.
	comments map[string][]*ast.CommentGroup
}

// linkFlag collects the -link options, mapping an import path prefix to the
// base URL of its manual.
type linkFlag map[string]string

func (l linkFlag) String() string { return "" }

func (l linkFlag) Set(s string) error {
	prefix, url, ok := strings.Cut(s, "=")
	if !ok || prefix == "" || url == "" {
		return errors.New("want PATH=URL")
	}
	l[prefix] = url
	return nil
}

func main() {
	out := flag.String("out", "", "the directory to write pages into")
	source := flag.String("source", "", "the URL of the module's source, for links to definitions")
	links := linkFlag{}
	flag.Var(links, "link", "the URL of the manual for an import path prefix, as PATH=URL")
	flag.Parse()

	if *out == "" {
		fmt.Fprintln(os.Stderr, "usage: godocmd -out DIR [-source URL] [-link PATH=URL ...] [packages]")
		os.Exit(2)
	}

	patterns := flag.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}

	if err := run(*out, *source, links, patterns); err != nil {
		fmt.Fprintln(os.Stderr, "godocmd:", err)
		os.Exit(1)
	}
}

func run(out, source string, links linkFlag, patterns []string) error {
	listed, err := listPackages(patterns)
	if err != nil {
		return err
	}

	var pages []*page
	byPath := map[string]*page{}
	for _, lp := range listed {
		if lp.Name == "main" || lp.Module == nil {
			continue
		}
		p, err := loadPage(lp)
		if err != nil {
			return err
		}
		pages = append(pages, p)
		byPath[lp.ImportPath] = p
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].rel < pages[j].rel })

	for _, p := range pages {
		w := &writer{page: p, pages: byPath, links: links, source: source}
		if err := writeFile(filepath.Join(out, p.rel+".md"), w.render()); err != nil {
			return err
		}
	}

	return writeFile(filepath.Join(out, "index.md"), renderIndex(pages))
}

// listPackages runs go list to find the packages matching patterns.  It is
// left to go list to say which files are in a package, so that build
// constraints are honored as they are when the package is built.
func listPackages(patterns []string) ([]listedPackage, error) {
	args := append([]string{"list", "-json"}, patterns...)
	cmd := exec.Command("go", args...)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}

	var pkgs []listedPackage
	dec := json.NewDecoder(bytes.NewReader(stdout))
	for {
		var lp listedPackage
		if err := dec.Decode(&lp); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("reading go list output: %w", err)
		}
		pkgs = append(pkgs, lp)
	}
	return pkgs, nil
}

func loadPage(lp listedPackage) (*page, error) {
	fset := token.NewFileSet()
	comments := map[string][]*ast.CommentGroup{}

	var files []*ast.File
	for _, name := range append(lp.GoFiles, lp.CgoFiles...) {
		filename := filepath.Join(lp.Dir, name)
		f, err := parser.ParseFile(fset, filename, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
		comments[filename] = f.Comments
	}

	docs, err := doc.NewFromFiles(fset, files, lp.ImportPath)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", lp.ImportPath, err)
	}

	rel := strings.TrimPrefix(strings.TrimPrefix(lp.ImportPath, lp.Module.Path), "/")
	if rel == "" {
		rel = lp.Name
	}

	return &page{pkg: lp, rel: rel, docs: docs, fset: fset, comments: comments}, nil
}

func writeFile(name string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o777); err != nil {
		return err
	}
	return os.WriteFile(name, content, 0o666)
}

func renderIndex(pages []*page) []byte {
	var b bytes.Buffer
	b.WriteString("# Packages\n\n")
	b.WriteString("| Package | |\n|---|---|\n")
	for _, p := range pages {
		synopsis := p.docs.Synopsis(p.docs.Doc)
		fmt.Fprintf(&b, "| [%s](%s.md) | %s |\n", p.rel, p.rel, escapeCell(synopsis))
	}
	return b.Bytes()
}

func escapeCell(s string) string {
	return strings.ReplaceAll(s, "|", `\|`)
}

// writer renders one package's page.
type writer struct {
	page   *page
	pages  map[string]*page
	links  linkFlag
	source string
	b      bytes.Buffer
}

func (w *writer) render() []byte {
	d := w.page.docs

	fmt.Fprintf(&w.b, "# %s\n\n", w.page.rel)
	fmt.Fprintf(&w.b, "```go\nimport %q\n```\n\n", d.ImportPath)
	w.comment(d.Doc, 2)

	w.values("Constants", d.Consts)
	w.values("Variables", d.Vars)

	for _, f := range d.Funcs {
		w.function(f, 2)
	}

	for _, t := range d.Types {
		w.heading(2, "type "+t.Name, t.Name)
		w.decl(t.Decl)
		w.comment(t.Doc, 3)
		for _, v := range t.Consts {
			w.value(v)
		}
		for _, v := range t.Vars {
			w.value(v)
		}
		for _, f := range t.Funcs {
			w.function(f, 3)
		}
		for _, f := range t.Methods {
			w.function(f, 3)
		}
	}

	return w.b.Bytes()
}

func (w *writer) values(title string, values []*doc.Value) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(&w.b, "## %s { #pkg-%s }\n\n", title, strings.ToLower(title))
	for _, v := range values {
		w.value(v)
	}
}

// value writes a group of constants or variables.  Having no heading of its
// own, each name in it is given its anchor by an empty HTML element.
func (w *writer) value(v *doc.Value) {
	for _, name := range v.Names {
		fmt.Fprintf(&w.b, "<a id=\"%s\"></a>\n", name)
	}
	w.b.WriteString("\n")
	w.decl(v.Decl)
	w.comment(v.Doc, 4)
}

// function writes a function or method, under a heading that names a
// method's receiver type as pkg.go.dev does: "func (*Directory) Lookup".
func (w *writer) function(f *doc.Func, level int) {
	title, anchor := "func "+f.Name, f.Name
	if f.Recv != "" {
		recv, _, _ := strings.Cut(strings.TrimPrefix(f.Recv, "*"), "[")
		title = fmt.Sprintf("func (%s) %s", f.Recv, f.Name)
		anchor = recv + "." + f.Name
	}
	w.heading(level, title, anchor)
	w.decl(f.Decl)
	w.comment(f.Doc, level+1)
}

// heading writes a Markdown heading with an explicit anchor, by way of the
// attr_list extension.
func (w *writer) heading(level int, title, anchor string) {
	fmt.Fprintf(&w.b, "%s `%s` { #%s }\n\n", strings.Repeat("#", level), title, anchor)
}

func (w *writer) decl(decl ast.Decl) {
	var code bytes.Buffer
	node := &printer.CommentedNode{Node: decl, Comments: keptComments(decl)}
	cfg := printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}
	if err := cfg.Fprint(&code, w.page.fset, node); err != nil {
		// Printing a node that the parser produced does not fail, short of
		// a bug in go/printer.
		panic(err)
	}

	fmt.Fprintf(&w.b, "```go\n%s\n```\n\n", code.String())

	if w.source != "" {
		pos := w.page.fset.Position(decl.Pos())
		file, err := filepath.Rel(w.page.pkg.Module.Dir, pos.Filename)
		if err == nil {
			url := strings.TrimSuffix(w.source, "/") + "/" + filepath.ToSlash(file)
			fmt.Fprintf(&w.b, "<small>[source](%s#L%d)</small>\n\n", url, pos.Line)
		}
	}
}

// keptComments returns the comments on the parts of decl that go/doc has
// left in it.  The file's other comments can't be given to the printer,
// because the comments on unexported fields, which go/doc removes, would be
// printed where the fields were.  Where go/doc has removed fields or
// methods, the printer is meant to say so, but it says so only when given
// no comments at all, so this adds the note itself, in pkg.go.dev's words.
func keptComments(decl ast.Decl) []*ast.CommentGroup {
	var kept []*ast.CommentGroup
	keep := func(groups ...*ast.CommentGroup) {
		for _, g := range groups {
			if g != nil {
				kept = append(kept, g)
			}
		}
	}

	ast.Inspect(decl, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Field:
			keep(n.Doc, n.Comment)
		case *ast.ValueSpec:
			keep(n.Doc, n.Comment)
		case *ast.TypeSpec:
			keep(n.Comment)
		case *ast.StructType:
			if n.Incomplete {
				keep(note(n.Fields, "fields"))
				n.Incomplete = false
			}
		case *ast.InterfaceType:
			if n.Incomplete {
				keep(note(n.Methods, "methods"))
				n.Incomplete = false
			}
		}
		return true
	})

	sort.Slice(kept, func(i, j int) bool { return kept[i].Pos() < kept[j].Pos() })
	return kept
}

// note returns a comment saying that some of a struct's fields or an
// interface's methods have been left out.  It is placed just before the
// closing brace, so that the printer puts it after the rest.
func note(list *ast.FieldList, what string) *ast.CommentGroup {
	text := "// contains filtered or unexported " + what
	return &ast.CommentGroup{List: []*ast.Comment{{Slash: list.Closing - 1, Text: text}}}
}

// comment writes a doc comment as Markdown, with any headings in it at
// level and below, and its doc links resolved as the command's
// documentation describes.
func (w *writer) comment(text string, level int) {
	if text == "" {
		return
	}
	pr := &comment.Printer{
		HeadingLevel: level,
		DocLinkURL:   w.linkURL,
	}
	w.b.Write(pr.Markdown(w.page.docs.Parser().Parse(text)))
	w.b.WriteString("\n")
}

func (w *writer) linkURL(l *comment.DocLink) string {
	anchor := l.Name
	if l.Recv != "" {
		anchor = l.Recv + "." + l.Name
	}
	frag := ""
	if anchor != "" {
		frag = "#" + anchor
	}

	if l.ImportPath == "" {
		return frag
	}

	if target, ok := w.pages[l.ImportPath]; ok {
		rel, err := filepath.Rel(filepath.Dir(w.page.rel), target.rel+".md")
		if err == nil {
			return filepath.ToSlash(rel) + frag
		}
	}

	var best string
	for prefix := range w.links {
		if (l.ImportPath == prefix || strings.HasPrefix(l.ImportPath, prefix+"/")) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best != "" {
		url := strings.TrimSuffix(w.links[best], "/")
		if rest := strings.TrimPrefix(l.ImportPath, best); rest != "" {
			url += rest
		}
		return url + "/" + frag
	}

	return "https://pkg.go.dev/" + l.ImportPath + frag
}

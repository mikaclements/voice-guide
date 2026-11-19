// Command classgen reads Go packages and writes their named structs and interfaces as JSON: each
// type's fields and methods, with doc comments and source positions, and the relationships
// between them: implements (a concrete type satisfies an interface, through its pointer or not),
// holds (a field whose type, behind pointers, slices, maps or channels, is another type in scope)
// and embeds.
//
// It needs nothing but the standard library and the go command: `go list -export -deps` names
// every package's source files and compiled export data; the packages asked for, and any package
// between them, are type-checked from source (so doc comments survive and one package has one
// identity), and everything else is imported from export data.
//
// Usage:
//
//	classgen -dir REPO [-exclude REGEXP] [-o FILE] PATTERN...
//
// It writes the schematic's class views, as scripts/schematic.sh runs it, and its tests check that the
// schematic's hand-written levels name every package and draw every import between the parts of
// voice.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// listed is the part of `go list -json` this program reads.
type listed struct {
	ImportPath string
	Name       string
	Dir        string
	GoFiles    []string
	Export     string
	DepOnly    bool
	Standard   bool
	Imports    []string
	Module     *struct{ Path, Dir string }
	Error      *struct{ Err string }
}

// Output is the JSON document written.
type Output struct {
	Generator string     `json:"generator"`
	Generated string     `json:"generated"`
	Module    string     `json:"module"`
	Patterns  []string   `json:"patterns"`
	Exclude   string     `json:"exclude,omitempty"`
	Packages  []Package  `json:"packages"`
	Types     []Type     `json:"types"`
	Relations []Relation `json:"relations"`
	Warnings  []string   `json:"warnings,omitempty"`
}

type Package struct {
	// Path is the import path relative to the module, such as internal/conversation/http.
	Path string `json:"path"`
	Name string `json:"name"`
	Doc  string `json:"doc"`
}

type Type struct {
	ID       string   `json:"id"`
	Package  string   `json:"package"`
	Name     string   `json:"name"`
	Kind     string   `json:"kind"` // struct or interface
	Exported bool     `json:"exported"`
	Doc      string   `json:"doc"`
	File     string   `json:"file"`
	Line     int      `json:"line"`
	Fields   []Field  `json:"fields"`
	Methods  []Method `json:"methods"`
	// Constructors are the package's functions that return this type or a pointer to it.
	Constructors []Method `json:"constructors,omitempty"`
	// External marks a type of the same module, outside the packages read, that a type in them
	// holds; it is written with no members.
	External bool `json:"external,omitempty"`
}

type Field struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Short    string `json:"short"`
	Exported bool   `json:"exported"`
	Embedded bool   `json:"embedded,omitempty"`
	Doc      string `json:"doc,omitempty"`
}

type Method struct {
	Name      string `json:"name"`
	Signature string `json:"signature"`
	Short     string `json:"short"`
	Exported  bool   `json:"exported"`
	Pointer   bool   `json:"pointer,omitempty"`
	Doc       string `json:"doc,omitempty"`
	Line      int    `json:"line,omitempty"`
}

type Relation struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"` // implements, holds or embeds
	// Via names the fields that hold or embed, for holds and embeds.
	Via []string `json:"via,omitempty"`
	// Many is set when a held type sits in a slice, array, map or channel.
	Many bool `json:"many,omitempty"`
	// Pointer is set when only the pointer type implements the interface.
	Pointer bool `json:"pointer,omitempty"`
}

func main() {
	dir := flag.String("dir", ".", "directory of the module to read, where `go list` runs")
	exclude := flag.String("exclude", "", "regular expression on module-relative package paths to leave out of the output")
	out := flag.String("o", "-", "file to write, - for standard output")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: classgen -dir REPO [-exclude REGEXP] [-o FILE] PATTERN...\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	var skip *regexp.Regexp
	if *exclude != "" {
		var err error
		if skip, err = regexp.Compile(*exclude); err != nil {
			fail(fmt.Errorf("-exclude: %w", err))
		}
	}
	doc, err := generate(*dir, flag.Args(), skip)
	if err != nil {
		fail(err)
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fail(err)
	}
	b = append(b, '\n')
	if *out == "-" {
		_, err = os.Stdout.Write(b)
	} else if err = os.MkdirAll(filepath.Dir(*out), 0o755); err == nil {
		err = os.WriteFile(*out, b, 0o644)
	}
	if err != nil {
		fail(err)
	}
	// A package that didn't type-check leaves its view short of types; say so where it's seen.
	for _, w := range doc.Warnings {
		fmt.Fprintln(os.Stderr, "classgen: warning:", w)
	}
	fmt.Fprintf(os.Stderr, "classgen: %d packages, %d types, %d relations\n", len(doc.Packages), len(doc.Types), len(doc.Relations))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "classgen:", err)
	os.Exit(1)
}

func goList(dir string, patterns []string) ([]listed, error) {
	args := append([]string{"list", "-e", "-export", "-deps",
		"-json=ImportPath,Name,Dir,GoFiles,Export,DepOnly,Standard,Imports,Module,Error"}, patterns...)
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var pkgs []listed
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		var p listed
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("go list output: %w", err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

// sourceImporter hands out packages type-checked from source where there is one, so a type keeps
// one identity across the packages read, and export data for everything else.
type sourceImporter struct {
	checked map[string]*types.Package
	gc      types.Importer
}

func (s *sourceImporter) Import(path string) (*types.Package, error) {
	if p, ok := s.checked[path]; ok {
		return p, nil
	}
	return s.gc.Import(path)
}

// checkedPkg is one package type-checked from source, with its syntax for the doc comments.
type checkedPkg struct {
	list  listed
	types *types.Package
	files []*ast.File
}

func generate(dir string, patterns []string, skip *regexp.Regexp) (*Output, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	pkgs, err := goList(absDir, patterns)
	if err != nil {
		return nil, err
	}
	out := &Output{Generator: "classgen", Generated: time.Now().UTC().Format(time.RFC3339), Patterns: patterns}
	if skip != nil {
		out.Exclude = skip.String()
	}
	exports := map[string]string{}
	for _, p := range pkgs {
		if p.Export != "" {
			exports[p.ImportPath] = p.Export
		}
		if !p.DepOnly && p.Module != nil && out.Module == "" {
			out.Module = p.Module.Path
		}
	}
	// A package is read from source when it was asked for, or when it imports one that is, so a
	// type has one identity wherever it appears. `go list -deps` lists dependencies first.
	fromSource := map[string]bool{}
	for _, p := range pkgs {
		if p.Standard {
			continue
		}
		need := !p.DepOnly
		for _, imp := range p.Imports {
			need = need || fromSource[imp]
		}
		fromSource[p.ImportPath] = need
	}
	fset := token.NewFileSet()
	gc := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		f, ok := exports[path]
		if !ok {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(f)
	})
	imp := &sourceImporter{checked: map[string]*types.Package{}, gc: gc}
	var targets []*checkedPkg
	for _, p := range pkgs {
		if !fromSource[p.ImportPath] {
			continue
		}
		if p.Error != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("%s: %s", p.ImportPath, p.Error.Err))
		}
		var files []*ast.File
		for _, name := range p.GoFiles {
			f, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, parser.ParseComments|parser.SkipObjectResolution)
			if err != nil {
				return nil, err
			}
			files = append(files, f)
		}
		conf := types.Config{Importer: imp, Error: func(err error) {
			out.Warnings = append(out.Warnings, err.Error())
		}}
		tp, _ := conf.Check(p.ImportPath, fset, files, nil)
		imp.checked[p.ImportPath] = tp
		if p.DepOnly || (skip != nil && skip.MatchString(rel(out.Module, p.ImportPath))) {
			continue
		}
		targets = append(targets, &checkedPkg{list: p, types: tp, files: files})
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no packages match %s", strings.Join(patterns, " "))
	}

	type entry struct {
		obj   *types.TypeName
		named *types.Named
		out   *Type
	}
	var entries []*entry
	byObj := map[*types.TypeName]*entry{}
	for _, cp := range targets {
		docs := collectDocs(cp.files)
		pkgPath := rel(out.Module, cp.list.ImportPath)
		out.Packages = append(out.Packages, Package{Path: pkgPath, Name: cp.types.Name(), Doc: packageDoc(cp.files)})
		q := qualifier(cp.types)
		scope := cp.types.Scope()
		var local []*entry
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || tn.IsAlias() {
				continue
			}
			named, ok := tn.Type().(*types.Named)
			if !ok {
				continue
			}
			var kind string
			switch named.Underlying().(type) {
			case *types.Struct:
				kind = "struct"
			case *types.Interface:
				kind = "interface"
			default:
				continue
			}
			pos := fset.Position(tn.Pos())
			t := &Type{
				ID:       pkgPath + "." + name,
				Package:  pkgPath,
				Name:     name,
				Kind:     kind,
				Exported: tn.Exported(),
				Doc:      docs[tn.Pos()],
				File:     relFile(absDir, pos.Filename),
				Line:     pos.Line,
				Fields:   []Field{},
				Methods:  []Method{},
			}
			switch u := named.Underlying().(type) {
			case *types.Struct:
				for i := range u.NumFields() {
					f := u.Field(i)
					t.Fields = append(t.Fields, Field{
						Name: f.Name(), Type: types.TypeString(f.Type(), q), Short: shortType(f.Type(), q),
						Exported: f.Exported(), Embedded: f.Embedded(), Doc: docs[f.Pos()],
					})
				}
				for i := range named.NumMethods() {
					m := named.Method(i)
					sig := m.Type().(*types.Signature)
					_, ptr := sig.Recv().Type().(*types.Pointer)
					t.Methods = append(t.Methods, method(m, sig, q, docs, fset, ptr))
				}
			case *types.Interface:
				for i := range u.NumEmbeddeds() {
					e := u.EmbeddedType(i)
					t.Fields = append(t.Fields, Field{Name: shortType(e, q), Type: types.TypeString(e, q), Short: shortType(e, q), Exported: true, Embedded: true})
				}
				for i := range u.NumExplicitMethods() {
					m := u.ExplicitMethod(i)
					t.Methods = append(t.Methods, method(m, m.Type().(*types.Signature), q, docs, fset, false))
				}
			}
			e := &entry{obj: tn, named: named, out: t}
			local = append(local, e)
			byObj[tn] = e
		}
		// Constructors: package functions returning the type or a pointer to it.
		for _, name := range scope.Names() {
			fn, ok := scope.Lookup(name).(*types.Func)
			if !ok {
				continue
			}
			sig := fn.Type().(*types.Signature)
			for i := range sig.Results().Len() {
				rt := sig.Results().At(i).Type()
				if p, ok := rt.(*types.Pointer); ok {
					rt = p.Elem()
				}
				if n, ok := rt.(*types.Named); ok {
					if e, ok := byObj[n.Obj()]; ok && n.Obj().Pkg() == cp.types {
						e.out.Constructors = append(e.out.Constructors, method(fn, sig, q, docs, fset, false))
						break
					}
				}
			}
		}
		// Exported types first, each group in source order.
		sort.SliceStable(local, func(i, j int) bool {
			a, b := local[i].out, local[j].out
			if a.Exported != b.Exported {
				return a.Exported
			}
			if a.File != b.File {
				return a.File < b.File
			}
			return a.Line < b.Line
		})
		entries = append(entries, local...)
	}
	for _, e := range entries {
		out.Types = append(out.Types, *e.out)
	}

	// A type of the same module held from outside the packages read, such as another module's
	// service, is written as a stub with no members, so the hold still shows.
	stubs := map[string]bool{}
	read := map[string]bool{}
	for _, cp := range targets {
		read[cp.list.ImportPath] = true
	}
	external := func(n *types.Named) (string, bool) {
		obj := n.Obj()
		if obj.Pkg() == nil || out.Module == "" || !strings.HasPrefix(obj.Pkg().Path(), out.Module+"/") {
			return "", false
		}
		path := rel(out.Module, obj.Pkg().Path())
		if read[obj.Pkg().Path()] || (skip != nil && skip.MatchString(path)) {
			return "", false
		}
		id := path + "." + obj.Name()
		if !stubs[id] {
			stubs[id] = true
			kind := "type"
			switch n.Underlying().(type) {
			case *types.Struct:
				kind = "struct"
			case *types.Interface:
				kind = "interface"
			}
			out.Types = append(out.Types, Type{ID: id, Package: path, Name: obj.Name(), Kind: kind,
				Exported: obj.Exported(), External: true, Fields: []Field{}, Methods: []Method{}})
		}
		return id, true
	}

	// Relations.
	rels := map[string]*Relation{}
	var order []string
	add := func(r Relation, via string) {
		key := r.From + "|" + r.To + "|" + r.Kind
		if have, ok := rels[key]; ok {
			if via != "" && !slices.Contains(have.Via, via) {
				have.Via = append(have.Via, via)
			}
			have.Many = have.Many || r.Many
			return
		}
		if via != "" {
			r.Via = []string{via}
		}
		rels[key] = &r
		order = append(order, key)
	}
	for _, e := range entries {
		switch u := e.named.Underlying().(type) {
		case *types.Struct:
			for i := range u.NumFields() {
				f := u.Field(i)
				if f.Embedded() {
					t := f.Type()
					if p, ok := t.(*types.Pointer); ok {
						t = p.Elem()
					}
					if n, ok := t.(*types.Named); ok {
						if to, ok := byObj[n.Obj()]; ok && to != e {
							add(Relation{From: e.out.ID, To: to.out.ID, Kind: "embeds"}, f.Name())
						}
					}
					continue
				}
				for _, h := range heldTypes(f.Type(), false, map[types.Type]bool{}) {
					if to, ok := byObj[h.named.Obj()]; ok && to != e {
						add(Relation{From: e.out.ID, To: to.out.ID, Kind: "holds", Many: h.many}, f.Name())
					} else if id, ok := external(h.named); ok {
						add(Relation{From: e.out.ID, To: id, Kind: "holds", Many: h.many}, f.Name())
					}
				}
			}
		case *types.Interface:
			for i := range u.NumEmbeddeds() {
				if n, ok := u.EmbeddedType(i).(*types.Named); ok {
					if to, ok := byObj[n.Obj()]; ok && to != e {
						add(Relation{From: e.out.ID, To: to.out.ID, Kind: "embeds"}, "")
					}
				}
			}
		}
	}
	for _, e := range entries {
		if e.out.Kind != "struct" || e.named.TypeParams().Len() > 0 {
			continue
		}
		for _, i := range entries {
			iface, ok := i.named.Underlying().(*types.Interface)
			if !ok || iface.NumMethods() == 0 || i.named.TypeParams().Len() > 0 {
				continue
			}
			if inherited(e.named, iface) {
				continue
			}
			switch {
			case types.Implements(e.named, iface):
				add(Relation{From: e.out.ID, To: i.out.ID, Kind: "implements"}, "")
			case types.Implements(types.NewPointer(e.named), iface):
				add(Relation{From: e.out.ID, To: i.out.ID, Kind: "implements", Pointer: true}, "")
			}
		}
	}
	for _, k := range order {
		out.Relations = append(out.Relations, *rels[k])
	}
	if out.Relations == nil {
		out.Relations = []Relation{}
	}
	return out, nil
}

// inherited reports whether a struct satisfies iface only through a field it embeds that
// satisfies it already, as a wrapper does; that relationship belongs to the embedded type.
func inherited(n *types.Named, iface *types.Interface) bool {
	st, ok := n.Underlying().(*types.Struct)
	if !ok {
		return false
	}
	for i := range st.NumFields() {
		f := st.Field(i)
		if !f.Embedded() {
			continue
		}
		t := f.Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		if types.Implements(t, iface) || types.Implements(types.NewPointer(t), iface) {
			return true
		}
	}
	return false
}

type held struct {
	named *types.Named
	many  bool
}

// heldTypes finds the named types a field's value holds: behind pointers, in slices, arrays, maps
// and channels, but not inside function signatures, which a field only calls.
func heldTypes(t types.Type, many bool, seen map[types.Type]bool) []held {
	if seen[t] {
		return nil
	}
	seen[t] = true
	switch t := t.(type) {
	case *types.Named:
		return []held{{named: t, many: many}}
	case *types.Alias:
		return heldTypes(types.Unalias(t), many, seen)
	case *types.Pointer:
		return heldTypes(t.Elem(), many, seen)
	case *types.Slice:
		return heldTypes(t.Elem(), true, seen)
	case *types.Array:
		return heldTypes(t.Elem(), true, seen)
	case *types.Chan:
		return heldTypes(t.Elem(), true, seen)
	case *types.Map:
		return append(heldTypes(t.Key(), true, seen), heldTypes(t.Elem(), true, seen)...)
	}
	return nil
}

func method(m *types.Func, sig *types.Signature, q types.Qualifier, docs map[token.Pos]string, fset *token.FileSet, ptr bool) Method {
	full := strings.TrimPrefix(types.TypeString(sig, q), "func")
	return Method{
		Name: m.Name(), Signature: full, Short: shortSignature(sig, q), Exported: m.Exported(),
		Pointer: ptr, Doc: docs[m.Pos()], Line: fset.Position(m.Pos()).Line,
	}
}

func qualifier(pkg *types.Package) types.Qualifier {
	return func(p *types.Package) string {
		if p == pkg {
			return ""
		}
		return p.Name()
	}
}

// shortSignature writes a signature for reading at a glance: parameter types without their
// names, a context as ctx, and results as types.
func shortSignature(sig *types.Signature, q types.Qualifier) string {
	var b strings.Builder
	b.WriteString("(")
	ps := sig.Params()
	for i := range ps.Len() {
		if i > 0 {
			b.WriteString(", ")
		}
		t := ps.At(i).Type()
		if sig.Variadic() && i == ps.Len()-1 {
			if s, ok := t.(*types.Slice); ok {
				b.WriteString("..." + shortType(s.Elem(), q))
				continue
			}
		}
		b.WriteString(shortType(t, q))
	}
	b.WriteString(")")
	rs := sig.Results()
	switch rs.Len() {
	case 0:
	case 1:
		b.WriteString(" " + shortType(rs.At(0).Type(), q))
	default:
		b.WriteString(" (")
		for i := range rs.Len() {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(shortType(rs.At(i).Type(), q))
		}
		b.WriteString(")")
	}
	return b.String()
}

func shortType(t types.Type, q types.Qualifier) string {
	switch t := t.(type) {
	case *types.Named:
		if obj := t.Obj(); obj.Pkg() != nil && obj.Pkg().Path() == "context" && obj.Name() == "Context" {
			return "ctx"
		}
		s := types.TypeString(t.Origin(), q)
		if i := strings.IndexByte(s, '['); i >= 0 {
			s = s[:i]
		}
		if args := t.TypeArgs(); args != nil && args.Len() > 0 {
			var parts []string
			for i := range args.Len() {
				parts = append(parts, shortType(args.At(i), q))
			}
			s += "[" + strings.Join(parts, ", ") + "]"
		}
		return s
	case *types.Alias:
		return types.TypeString(t, q)
	case *types.Pointer:
		return "*" + shortType(t.Elem(), q)
	case *types.Slice:
		return "[]" + shortType(t.Elem(), q)
	case *types.Array:
		return fmt.Sprintf("[%d]%s", t.Len(), shortType(t.Elem(), q))
	case *types.Map:
		return "map[" + shortType(t.Key(), q) + "]" + shortType(t.Elem(), q)
	case *types.Chan:
		switch t.Dir() {
		case types.SendOnly:
			return "chan<- " + shortType(t.Elem(), q)
		case types.RecvOnly:
			return "<-chan " + shortType(t.Elem(), q)
		}
		return "chan " + shortType(t.Elem(), q)
	case *types.Signature:
		return "func" + shortSignature(t, q)
	case *types.Struct:
		if t.NumFields() == 0 {
			return "struct{}"
		}
		return "struct{…}"
	case *types.Interface:
		if t.Empty() {
			return "any"
		}
		return "interface{…}"
	}
	return types.TypeString(t, q)
}

// collectDocs maps the position of each declared name (type, field, function, method, interface
// method) to its doc comment, or its line comment when it has none.
func collectDocs(files []*ast.File) map[token.Pos]string {
	docs := map[token.Pos]string{}
	text := func(groups ...*ast.CommentGroup) string {
		for _, g := range groups {
			if g != nil {
				if s := strings.TrimSpace(g.Text()); s != "" {
					return s
				}
			}
		}
		return ""
	}
	fields := func(list *ast.FieldList) {
		if list == nil {
			return
		}
		for _, f := range list.List {
			d := text(f.Doc, f.Comment)
			if d == "" {
				continue
			}
			for _, n := range f.Names {
				docs[n.Pos()] = d
			}
			if len(f.Names) == 0 {
				// An embedded field's position is its type's.
				docs[f.Type.Pos()] = d
				if s, ok := f.Type.(*ast.StarExpr); ok {
					docs[s.X.Pos()] = d
				}
			}
		}
	}
	for _, f := range files {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if s := text(d.Doc); s != "" {
					docs[d.Name.Pos()] = s
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					groups := []*ast.CommentGroup{ts.Doc}
					if len(d.Specs) == 1 {
						groups = append(groups, d.Doc)
					}
					groups = append(groups, ts.Comment)
					if s := text(groups...); s != "" {
						docs[ts.Name.Pos()] = s
					}
					switch t := ts.Type.(type) {
					case *ast.StructType:
						fields(t.Fields)
					case *ast.InterfaceType:
						fields(t.Methods)
					}
				}
			}
		}
	}
	return docs
}

func packageDoc(files []*ast.File) string {
	best := ""
	for _, f := range files {
		if f.Doc == nil {
			continue
		}
		// A package has its doc comment in one file; take the longest if several carry one.
		if s := strings.TrimSpace(f.Doc.Text()); len(s) > len(best) {
			best = s
		}
	}
	return best
}

func rel(module, path string) string {
	if module != "" && strings.HasPrefix(path, module+"/") {
		return strings.TrimPrefix(path, module+"/")
	}
	return path
}

func relFile(dir, file string) string {
	if r, err := filepath.Rel(dir, file); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return file
}

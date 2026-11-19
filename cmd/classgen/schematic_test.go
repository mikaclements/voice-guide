package main

// The schematic's drift check, run with every other test. The build fails when the schematic:
//   - doesn't name a Go package, or names a package, file or path that no longer exists;
//   - has a package outside every part of voice at its second level;
//   - draws no edge for an import between two of those parts;
//   - lists a port's methods differently from the interface in the code.
// So the change that adds or alters one also draws it. How to update the schematic: the
// schematic skill.

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
)

const (
	repoRoot      = "../.."
	schematicFile = "schematic/data/schematic.json"
	modulePrefix  = "github.com/mikaclements/voice-guide/"
)

// generatedDB matches the packages sqlc generates, which their store stands for.
var generatedDB = regexp.MustCompile(`/store/postgres/db$`)

type schematicNode struct {
	ID             string
	Kind           string
	Name           string
	Path           string
	Parent         string
	Focus          string
	Members        []string
	Context        bool
	ImportedFreely bool
}

type schematicView struct {
	ID    string
	Level int
	Nodes []schematicNode
	Edges []struct{ From, To string }
}

type schematicData struct {
	Omitted map[string]string
	Views   []schematicView
}

func loadSchematic(t *testing.T) schematicData {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, schematicFile))
	if err != nil {
		t.Fatalf("read the schematic: %v", err)
	}
	var s schematicData
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("parse %s: %v", schematicFile, err)
	}
	return s
}

// readSources reads every Go file under internal/ and cmd/. The test only needs go list's answer,
// but go test caches a passing test until a file the test itself opened changes; reading them here
// makes a new package, file or import run the check again rather than pass from the cache.
func readSources(t *testing.T) {
	t.Helper()
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
				return err
			}
			_, err = os.ReadFile(p)
			return err
		})
		if err != nil {
			t.Fatalf("read the Go sources: %v", err)
		}
	}
}

var (
	packagesOnce sync.Once
	packagesList map[string][]string
	packagesErr  string
)

// goPackages lists every package under internal/ and cmd/, module-relative, with the packages of
// the module each imports outside its tests, leaving out the code sqlc generates.
func goPackages(t *testing.T) map[string][]string {
	t.Helper()
	readSources(t)
	packagesOnce.Do(func() {
		cmd := exec.Command("go", "list", "-f", `{{.ImportPath}}{{range .Imports}} {{.}}{{end}}`, "./internal/...", "./cmd/...")
		cmd.Dir = repoRoot
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			packagesErr = err.Error() + ": " + stderr.String()
			return
		}
		packagesList = map[string][]string{}
		for line := range strings.Lines(string(out)) {
			fields := strings.Fields(line)
			if len(fields) == 0 || generatedDB.MatchString(fields[0]) {
				continue
			}
			var imports []string
			for _, imp := range fields[1:] {
				if strings.HasPrefix(imp, modulePrefix) {
					imports = append(imports, strings.TrimPrefix(imp, modulePrefix))
				}
			}
			packagesList[strings.TrimPrefix(fields[0], modulePrefix)] = imports
		}
	})
	if packagesErr != "" {
		t.Fatalf("go list: %s", packagesErr)
	}
	return packagesList
}

// pathParts splits a node's path, which may name several things joined by " · ".
func pathParts(p string) []string {
	var parts []string
	for part := range strings.SplitSeq(p, " · ") {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func isGoPath(part string) bool {
	return strings.HasPrefix(part, "internal/") || strings.HasPrefix(part, "cmd/")
}

func exists(rel string) bool {
	_, err := os.Stat(filepath.Join(repoRoot, rel))
	return err == nil
}

// moduleRoot is a third level's package: the path of its outermost group, the box it draws.
func moduleRoot(v schematicView) string {
	for _, n := range v.Nodes {
		if n.Kind == "group" && n.Parent == "" && isGoPath(n.Path) {
			return n.Path
		}
	}
	return ""
}

func TestSchematicNamesEveryPackage(t *testing.T) {
	s := loadSchematic(t)
	var named []string
	for _, v := range s.Views {
		for _, n := range v.Nodes {
			named = append(named, pathParts(n.Path)...)
		}
	}
	var missing []string
	for pkg := range goPackages(t) {
		if _, ok := s.Omitted[pkg]; ok {
			continue
		}
		if !slices.ContainsFunc(named, func(part string) bool {
			ok, _ := path.Match(part, pkg)
			return ok
		}) {
			missing = append(missing, pkg)
		}
	}
	sort.Strings(missing)
	for _, pkg := range missing {
		t.Errorf("%s isn't in the schematic: draw it at its level in %s, or list it under omitted with why", pkg, schematicFile)
	}
}

func TestSchematicNamesOnlyWhatExists(t *testing.T) {
	s := loadSchematic(t)
	pkgs := goPackages(t)
	isPackage := func(p string) bool { _, ok := pkgs[p]; return ok }
	for _, v := range s.Views {
		root := moduleRoot(v)
		for _, n := range v.Nodes {
			where := "view " + v.ID + ", node " + n.ID
			if n.Focus != "" && !isPackage(n.Focus) {
				t.Errorf("%s: focus %s is no package", where, n.Focus)
			}
			for _, part := range pathParts(n.Path) {
				switch {
				case strings.Contains(part, "*") && isGoPath(part):
					if !slices.ContainsFunc(slices.Collect(keys(pkgs)), func(pkg string) bool {
						ok, _ := path.Match(part, pkg)
						return ok
					}) {
						t.Errorf("%s: %s matches no package", where, part)
					}
				case isGoPath(part) && (strings.HasSuffix(part, ".go") || strings.HasSuffix(part, "/")):
					if !exists(part) {
						t.Errorf("%s: %s doesn't exist", where, part)
					}
				case isGoPath(part):
					if !isPackage(part) {
						t.Errorf("%s: no package %s", where, part)
					}
				case strings.HasSuffix(part, ".go") && !strings.Contains(part, "/") && root != "":
					// A file of the module the third level draws, in the card's own package or the core's.
					if !exists(path.Join(n.Focus, part)) && !exists(path.Join(root, part)) {
						t.Errorf("%s: no file %s in %s", where, part, root)
					}
				}
			}
		}
	}
	for pkg := range s.Omitted {
		if !isPackage(pkg) {
			t.Errorf("omitted lists %s, which no longer exists", pkg)
		}
	}
}

func TestSchematicDrawsEveryImportBetweenTheParts(t *testing.T) {
	s := loadSchematic(t)
	// The parts of voice are the second level's own cards whose path is a folder of packages;
	// cmd/voice, named by its file, isn't one: it wires them all, and only its own calls are drawn.
	type part struct{ id, root string }
	var parts []part
	freely := map[string]bool{}
	edges := map[[2]string]bool{}
	for _, v := range s.Views {
		if v.Level != 2 {
			continue
		}
		for _, n := range v.Nodes {
			if n.Kind == "group" || n.Context {
				continue
			}
			for _, p := range pathParts(n.Path) {
				if isGoPath(p) && !strings.HasSuffix(p, ".go") && !strings.Contains(p, "*") {
					parts = append(parts, part{n.ID, strings.TrimSuffix(p, "/")})
				}
			}
			freely[n.ID] = n.ImportedFreely
		}
		for _, e := range v.Edges {
			edges[[2]string{e.From, e.To}] = true
			edges[[2]string{e.To, e.From}] = true
		}
	}
	if len(parts) == 0 {
		t.Fatal("the schematic has no second level with parts of voice")
	}
	// The longest root first, so a package belongs to the most specific part.
	sort.Slice(parts, func(i, j int) bool { return len(parts[i].root) > len(parts[j].root) })
	owner := func(pkg string) string {
		for _, p := range parts {
			if pkg == p.root || strings.HasPrefix(pkg, p.root+"/") {
				return p.id
			}
		}
		return ""
	}
	pkgs := goPackages(t)
	names := slices.Sorted(keys(pkgs))
	seen := map[[2]string]bool{}
	for _, pkg := range names {
		_, omitted := s.Omitted[pkg]
		from := owner(pkg)
		if from == "" && !omitted && strings.HasPrefix(pkg, "internal/") {
			t.Errorf("%s is inside no part of voice at the second level: draw it there, in %s", pkg, schematicFile)
		}
		for _, imp := range pkgs[pkg] {
			if _, ok := s.Omitted[imp]; ok && !strings.HasPrefix(pkg, "cmd/") {
				t.Errorf("%s imports %s, which the schematic omits: draw it, in %s", pkg, imp, schematicFile)
			}
			to := owner(imp)
			pair := [2]string{from, to}
			if from == "" || to == "" || from == to || freely[to] || edges[pair] || seen[pair] {
				continue
			}
			seen[pair] = true
			t.Errorf("%s imports %s, but the second level draws no edge between %s and %s: add one with what flows, in %s", pkg, imp, from, to, schematicFile)
		}
	}
}

func TestSchematicPortsListTheirMethods(t *testing.T) {
	s := loadSchematic(t)
	readSources(t)
	generatedFor := map[string]*Output{}
	for _, v := range s.Views {
		root := moduleRoot(v)
		if v.Level != 3 || root == "" {
			continue
		}
		for _, n := range v.Nodes {
			if n.Kind != "port" || len(n.Members) == 0 {
				continue
			}
			if generatedFor[root] == nil {
				out, err := generate(repoRoot, []string{"./" + root + "/..."}, generatedDB)
				if err != nil {
					t.Fatalf("read %s's types: %v", root, err)
				}
				generatedFor[root] = out
			}
			// A port's members are its interface's methods, one per line; a line "Name: A · B" lists
			// the methods of another interface the port declares, such as the connection it returns.
			pkg := cmpOr(n.Focus, root)
			want := map[string][]string{n.Name: nil}
			order := []string{n.Name}
			for _, m := range n.Members {
				if name, list, ok := strings.Cut(m, ": "); ok && !strings.ContainsAny(name, "( ") {
					want[name] = strings.Split(list, " · ")
					order = append(order, name)
					continue
				}
				want[n.Name] = append(want[n.Name], strings.FieldsFunc(m, func(r rune) bool { return r == '(' || r == ' ' })[0])
			}
			for _, iface := range order {
				i := slices.IndexFunc(generatedFor[root].Types, func(ty Type) bool {
					return ty.ID == pkg+"."+iface && ty.Kind == "interface"
				})
				if i < 0 {
					t.Errorf("view %s, port %s: no interface %s in %s", v.ID, n.ID, iface, pkg)
					continue
				}
				var inCode []string
				for _, m := range generatedFor[root].Types[i].Methods {
					inCode = append(inCode, m.Name)
				}
				drawn := slices.Clone(want[iface])
				sort.Strings(inCode)
				sort.Strings(drawn)
				if !slices.Equal(inCode, drawn) {
					t.Errorf("view %s, port %s lists %v for %s; the interface %s.%s has %v: update its members, in %s", v.ID, n.ID, drawn, iface, pkg, iface, inCode, schematicFile)
				}
			}
		}
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func keys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

package main

import (
	"regexp"
	"slices"
	"testing"
)

// fixture is a small module in testdata: shop declares a Store port and a Service holding it, and
// shop/memory implements the port through its pointer type.
const fixture = "testdata/fixture"

func generated(t *testing.T, patterns []string, skip *regexp.Regexp) *Output {
	t.Helper()
	out, err := generate(fixture, patterns, skip)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return out
}

func typeByID(t *testing.T, out *Output, id string) Type {
	t.Helper()
	for _, ty := range out.Types {
		if ty.ID == id {
			return ty
		}
	}
	t.Fatalf("no type %s in %v", id, out.Types)
	return Type{}
}

func hasRelation(out *Output, want Relation) bool {
	return slices.ContainsFunc(out.Relations, func(r Relation) bool {
		return r.From == want.From && r.To == want.To && r.Kind == want.Kind &&
			slices.Equal(r.Via, want.Via) && r.Many == want.Many && r.Pointer == want.Pointer
	})
}

func TestGenerateReadsTypesWithTheirMembersAndDocs(t *testing.T) {
	out := generated(t, []string{"./..."}, nil)

	if got := len(out.Packages); got != 2 || out.Packages[0].Path != "shop" || out.Packages[1].Path != "shop/memory" {
		t.Fatalf("packages = %+v, want shop and shop/memory", out.Packages)
	}
	if out.Packages[0].Doc != "Package shop sells things: the generator's test fixture." {
		t.Errorf("shop's doc = %q", out.Packages[0].Doc)
	}
	store := typeByID(t, out, "shop.Store")
	if store.Kind != "interface" || len(store.Methods) != 1 || store.Methods[0].Name != "Save" {
		t.Errorf("shop.Store = %+v, want an interface with Save", store)
	}
	order := typeByID(t, out, "shop.Order")
	var note Field
	for _, f := range order.Fields {
		if f.Name == "note" {
			note = f
		}
	}
	if note.Name == "" || note.Exported {
		t.Errorf("Order's fields = %+v, want note unexported", order.Fields)
	}
	service := typeByID(t, out, "shop.Service")
	if service.Doc != "Service sells through a store." {
		t.Errorf("Service's doc = %q", service.Doc)
	}
	if len(service.Constructors) != 1 || service.Constructors[0].Name != "NewService" {
		t.Errorf("Service's constructors = %+v, want NewService", service.Constructors)
	}
}

func TestGenerateFindsImplementsHoldsAndEmbeds(t *testing.T) {
	out := generated(t, []string{"./..."}, nil)

	for _, want := range []Relation{
		{From: "shop/memory.Store", To: "shop.Store", Kind: "implements", Pointer: true},
		{From: "shop.Service", To: "shop.Store", Kind: "holds", Via: []string{"store"}},
		{From: "shop.Service", To: "shop.Clock", Kind: "embeds", Via: []string{"Clock"}},
		{From: "shop.Order", To: "shop.Line", Kind: "holds", Via: []string{"Lines"}, Many: true},
		{From: "shop.Line", To: "shop.Priced", Kind: "implements"},
	} {
		if !hasRelation(out, want) {
			t.Errorf("no relation %+v in %+v", want, out.Relations)
		}
	}
}

func TestGenerateDrawsAHeldTypeOutsideThePackagesReadAsAStub(t *testing.T) {
	out := generated(t, []string{"./shop/memory"}, nil)

	order := typeByID(t, out, "shop.Order")
	if !order.External || len(order.Fields) != 0 {
		t.Errorf("shop.Order = %+v, want an outside stub with no members", order)
	}
	if !hasRelation(out, Relation{From: "shop/memory.Store", To: "shop.Order", Kind: "holds", Via: []string{"orders"}, Many: true}) {
		t.Errorf("relations = %+v, want memory.Store holding shop.Order", out.Relations)
	}
}

func TestGenerateLeavesOutExcludedPackages(t *testing.T) {
	out := generated(t, []string{"./..."}, regexp.MustCompile(`/memory$`))

	for _, ty := range out.Types {
		if ty.Package == "shop/memory" {
			t.Errorf("type %s of an excluded package was written", ty.ID)
		}
	}
	for _, r := range out.Relations {
		if r.From == "shop/memory.Store" {
			t.Errorf("relation %+v from an excluded package was written", r)
		}
	}
}

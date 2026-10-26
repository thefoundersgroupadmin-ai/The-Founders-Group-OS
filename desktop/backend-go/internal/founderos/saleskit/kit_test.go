package saleskit

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func TestCatalogAndPortableBundle(t *testing.T) {
	skills := Catalog()
	if len(skills) != 37 {
		t.Fatalf("want 37 workflows, got %d", len(skills))
	}
	seen := map[string]bool{}
	for _, s := range skills {
		if seen[s.ID] || s.ID == "" || len(s.Method) < 30 || len(s.Output) < 10 {
			t.Fatalf("invalid workflow: %+v", s)
		}
		seen[s.ID] = true
		md, ok := Markdown(s.ID)
		if !ok || !strings.Contains(md, "name: sales-"+s.ID) {
			t.Fatal(s.ID)
		}
	}
	raw, err := Bundle()
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]bool{}
	for _, f := range z.File {
		files[f.Name] = true
	}
	for _, p := range []string{"sales-kit/README.md", "sales-kit/client-profile.example.json", "sales-kit/manifest.json", "sales-kit/skills/sales-draft-outreach/SKILL.md"} {
		if !files[p] {
			t.Fatalf("missing %s", p)
		}
	}
}

func TestPrepareValidatesScopeAndSources(t *testing.T) {
	valid := Input{Workspace: "client-alpha", Request: "Draft first outreach", Profile: "We sell inventory planning to wholesalers.", Sources: []Source{{ID: "lead-1", Title: "Demo enquiry", Text: "Jordan requested a demo on 2026-10-25."}}}
	p, err := Prepare("draft-outreach", valid)
	if err != nil || !strings.Contains(p, "lead-1") || !strings.Contains(p, "client-alpha") {
		t.Fatalf("%v %s", err, p)
	}
	for _, mutate := range []func(*Input){
		func(i *Input) { i.Workspace = "" }, func(i *Input) { i.Profile = "" }, func(i *Input) { i.Sources = nil },
		func(i *Input) { i.Sources = append(i.Sources, i.Sources[0]) }, func(i *Input) { i.Sources[0].Text = "" },
	} {
		i := valid
		i.Sources = append([]Source(nil), valid.Sources...)
		mutate(&i)
		if _, err := Prepare("draft-outreach", i); err == nil {
			t.Fatalf("accepted invalid input: %+v", i)
		}
	}
	if _, err := Prepare("../private", valid); err == nil {
		t.Fatal("accepted unknown workflow")
	}
}

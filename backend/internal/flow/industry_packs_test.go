package flow

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// The embedded skillpacks/*.json stay the runtime source of truth (frozen by
// hash per run). industry-packs/<id>/ at the repo root is the reviewable
// export the PRD asks for (§9.2): manifest, report schemas and eval sets.
// This test fails if the export drifts; regenerate with
//
//	go test ./internal/flow -run TestIndustryPackExport -update-packs
var updatePacks = flag.Bool("update-packs", false, "rewrite industry-packs/ from the embedded skill packs")

func industryPackFiles(t *testing.T) map[string][]byte {
	t.Helper()
	ps, err := SkillPacks()
	if err != nil {
		t.Fatal(err)
	}
	enc := func(v any) []byte {
		var b bytes.Buffer
		e := json.NewEncoder(&b)
		e.SetEscapeHTML(false)
		e.SetIndent("", "  ")
		if err := e.Encode(v); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	known := map[string]bool{}
	for _, s := range Catalog {
		known[s.Name] = true
	}
	out := map[string][]byte{}
	for _, p := range ps {
		for _, tool := range p.RequiredTools {
			if !known[tool] {
				t.Fatalf("pack %s requires unknown tool %s", p.ID, tool)
			}
		}
		var skills []map[string]string
		for _, s := range p.Skills {
			f := "schemas/" + s.ID + ".json"
			skills = append(skills, map[string]string{"id": s.ID, "version": s.Version, "hash": s.Hash, "output_schema": f})
			var schema any
			if err := json.Unmarshal(s.OutputSchema, &schema); err != nil {
				t.Fatalf("%s/%s output_schema: %v", p.ID, s.ID, err)
			}
			out[p.ID+"/"+f] = enc(schema)
		}
		m := map[string]any{
			"id": p.ID, "version": p.Version, "name": p.Name, "kind": p.Kind,
			"skills": skills, "required_tools": p.RequiredTools, "optional_connectors": p.OptionalConns,
			"source": "backend/internal/flow/skillpacks/" + p.ID + ".json",
			"note":   "generated from the embedded pack; edit the source and regenerate (see industry_packs_test.go)",
		}
		if len(p.ReportSchema) > 0 {
			var rs any
			_ = json.Unmarshal(p.ReportSchema, &rs)
			out[p.ID+"/schemas/report.json"] = enc(rs)
			m["report_schema"] = "schemas/report.json"
		}
		if len(p.EvalSamples) > 0 {
			var samples []json.RawMessage
			if err := json.Unmarshal(p.EvalSamples, &samples); err != nil {
				t.Fatal(err)
			}
			var b bytes.Buffer
			for _, s := range samples {
				var c bytes.Buffer
				_ = json.Compact(&c, s)
				b.Write(c.Bytes())
				b.WriteByte('\n')
			}
			out[p.ID+"/evals/evidence-cases.jsonl"] = b.Bytes()
			m["evaluation_set"] = "evals/evidence-cases.jsonl"
		}
		if len(p.SourceTemplates) > 0 {
			var st any
			_ = json.Unmarshal(p.SourceTemplates, &st)
			out[p.ID+"/source-templates.json"] = enc(st)
		}
		if len(p.Glossary) > 0 {
			var g any
			_ = json.Unmarshal(p.Glossary, &g)
			out[p.ID+"/glossary.json"] = enc(g)
		}
		out[p.ID+"/manifest.json"] = enc(m)
	}
	return out
}

func TestIndustryPackExport(t *testing.T) {
	root := filepath.Join("..", "..", "..", "industry-packs")
	want := industryPackFiles(t)
	if *updatePacks {
		_ = os.RemoveAll(root)
		for name, b := range want {
			p := filepath.Join(root, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	seen := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(path) == "README.md" {
			return err
		}
		seen++
		rel, _ := filepath.Rel(root, path)
		got, _ := os.ReadFile(path)
		if exp, ok := want[filepath.ToSlash(rel)]; !ok {
			t.Errorf("stale file industry-packs/%s", rel)
		} else if !bytes.Equal(got, exp) {
			t.Errorf("industry-packs/%s out of date; run with -update-packs", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != len(want) {
		t.Fatalf("industry-packs has %d files, want %d; run with -update-packs", seen, len(want))
	}
}

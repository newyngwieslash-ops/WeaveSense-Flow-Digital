package flow

import "testing"

func TestEnrichmentToolsRouteToEngine(t *testing.T) {
	want := map[string]string{"intel_extract_document": "/v1/documents/extract", "intel_transcribe_document": "/v1/documents/transcript"}
	for _, spec := range Catalog {
		path, ok := want[spec.Name]
		if !ok {
			continue
		}
		delete(want, spec.Name)
		if spec.Scope != "intel.collect" || spec.Class != "write" {
			t.Fatalf("%s: scope=%s class=%s", spec.Name, spec.Scope, spec.Class)
		}
		m, p, _ := spec.route(map[string]any{"document_id": "doc_1"})
		if m != "POST" || p != path {
			t.Fatalf("%s routes to %s %s", spec.Name, m, p)
		}
		if !ToolAllowedForSubject(SubjectTopic, spec.Name) {
			t.Fatalf("%s should be available to topic conversations", spec.Name)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing tools: %v", want)
	}
}

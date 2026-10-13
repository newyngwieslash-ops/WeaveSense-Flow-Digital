package flow

import (
	"net/http"
	"time"
)

func init() {
	Catalog = append(Catalog, ToolSpec{
		Name:        "intel_extract_document",
		Description: "Fetch the document's web page and extract its main text (on-demand). A longer, changed text becomes a new revision; old citations keep their revision.",
		Class:       "write", Scope: "intel.collect", Timeout: 30 * time.Second, OutputCap: 4000,
		Parameters: obj([]string{"document_id"}, map[string]any{"document_id": tStr}),
		route:      func(a map[string]any) (string, string, any) { return http.MethodPost, "/v1/documents/extract", a },
	})
}

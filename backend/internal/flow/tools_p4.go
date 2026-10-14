package flow

import (
	"net/http"
	"time"
)

func init() {
	Catalog = append(Catalog, ToolSpec{
		Name:        "intel_extract_document",
		Description: "Fetch the document's web page and extract its main text (on-demand). via=html (default) uses the guarded fetch; via=reader uses the operator-enabled article reader for allowlisted hosts. A longer, changed text becomes a new revision; old citations keep their revision.",
		Class:       "write", Scope: "intel.collect", Timeout: 30 * time.Second, OutputCap: 4000,
		Parameters: obj([]string{"document_id"}, map[string]any{"document_id": tStr, "via": map[string]any{"type": "string", "enum": []string{"html", "reader"}}}),
		route:      func(a map[string]any) (string, string, any) { return http.MethodPost, "/v1/documents/extract", a },
	}, ToolSpec{
		Name:        "intel_transcribe_document",
		Description: "For a YouTube video document, fetch its subtitles (operator-enabled subtitle worker) and append the transcript as a new revision so it can be searched and cited. language: en, zh-Hans or zh-Hant.",
		Class:       "write", Scope: "intel.collect", Timeout: 60 * time.Second, OutputCap: 4000,
		Parameters: obj([]string{"document_id"}, map[string]any{"document_id": tStr, "language": map[string]any{"type": "string", "enum": []string{"en", "zh-Hans", "zh-Hant"}}}),
		route:      func(a map[string]any) (string, string, any) { return http.MethodPost, "/v1/documents/transcript", a },
	}, ToolSpec{
		Name:        "intel_ocr_document",
		Description: "OCR the document's stored image attachments (operator-enabled OCR worker, Chinese + English). Recognised text is appended as a new revision per image; images already recognised are not re-run, failed ones are retried.",
		Class:       "write", Scope: "intel.collect", Timeout: 120 * time.Second, OutputCap: 4000,
		Parameters: obj([]string{"document_id"}, map[string]any{"document_id": tStr}),
		route:      func(a map[string]any) (string, string, any) { return http.MethodPost, "/v1/documents/ocr", a },
	})
	topicTools["intel_ocr_document"] = true
}

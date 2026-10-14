package flow

import (
	"net/http"
	"time"
)

// §8.3 first-batch tools that were still missing: intel_list_sources and intel_compare_evidence.
func init() {
	Catalog = append(Catalog, ToolSpec{
		Name:        "intel_list_sources",
		Description: "List the workspace's sources with health: status (active/paused/revoked), health (healthy/degraded/backoff/never_run), consecutive failures, last error, backoff, last run and cursor time. Read-only.",
		Class:       "read", Scope: "intel.sources.read", Timeout: 10 * time.Second, OutputCap: 16000,
		Parameters: obj([]string{}, map[string]any{}),
		route:      func(a map[string]any) (string, string, any) { return http.MethodGet, "/v1/sources/health", nil },
	}, ToolSpec{
		Name:        "intel_compare_evidence",
		Description: "Structured comparison of 2-20 evidence items (ids from search/research): grouped by source, numeric facts, numeric divergences between sources, shared terms, and stale (revised) evidence. Read-only, no model call; signals are lexical and must be verified before claiming agreement or conflict.",
		Class:       "read", Scope: "intel.documents.read", Timeout: 10 * time.Second, OutputCap: 16000,
		Parameters: obj([]string{"evidence_ids"}, map[string]any{"evidence_ids": tStrs}),
		route:      func(a map[string]any) (string, string, any) { return http.MethodPost, "/v1/evidence/compare", a },
	})
	topicTools["intel_list_sources"] = true
	topicTools["intel_compare_evidence"] = true
}

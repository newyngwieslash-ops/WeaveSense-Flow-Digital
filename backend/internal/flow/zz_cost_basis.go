package flow

// §8.3 every tool declares its cost basis (成本口径). zz_ file: runs after the
// other init() functions that append to Catalog.
var toolCostBasis = map[string]string{
	"intel_search_documents":    "free: engine read, no model or provider cost",
	"intel_read_document":       "free: engine read, no model or provider cost",
	"intel_list_sources":        "free: engine read",
	"intel_compare_evidence":    "free: engine read, lexical (no model call)",
	"intel_get_operation":       "free: projection/engine read",
	"intel_cancel_operation":    "free: releases open budget reservations of the operation",
	"intel_load_skill":          "free: platform read",
	"intel_save_topic":          "free: platform write",
	"intel_preview_monitor":     "free: returns an estimate (runs/day, collect cost per window in micro-credits) without reserving",
	"intel_apply_monitor":       "collect: per collection run, reserved per window against the monitor budget (budget_micro_per_window)",
	"intel_collect_once":        "collect: one collection run per source, reserved before dispatch",
	"intel_preview_schedule":    "free: returns next run times",
	"intel_apply_schedule":      "model: per brief window, summarization tokens via Model Gateway (reserved, price_version); extractive fallback is free",
	"intel_start_research":      "free extraction in the engine; optional synthesis tokens via Model Gateway (reserved, price_version)",
	"intel_save_report":         "free: lexical Reviewer verification (no model call); storage only",
	"intel_extract_document":    "extract: per document (usage_type=extract)",
	"intel_transcribe_document": "extract: per transcript fetch (usage_type=extract)",
	"intel_ocr_document":        "ocr: per page/image (usage_type=ocr)",
	"intel_asr_document":        "asr: per audio minute (usage_type=asr)",
	"intel_handoff_to_canvas":   "free: platform canvas write (no model or provider cost)",
}

func init() {
	for i := range Catalog {
		if Catalog[i].CostBasis == "" {
			Catalog[i].CostBasis = toolCostBasis[Catalog[i].Name]
		}
	}
	for i := range p3Tools {
		if p3Tools[i].CostBasis == "" {
			p3Tools[i].CostBasis = toolCostBasis[p3Tools[i].Name]
		}
	}
}

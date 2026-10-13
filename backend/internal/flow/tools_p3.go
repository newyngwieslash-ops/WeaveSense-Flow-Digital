package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"yingce/backend/internal/model"
)

func init() {
	Catalog = append(Catalog, p3Tools...)
}

func argStr(a map[string]any, k string) string {
	v, _ := a[k].(string)
	return v
}

// skillRefFor pins the skill for this run when skill_id is given.
func skillRefFor(t Tools, conv model.FlowAgentConversation, ss SubjectScope, a map[string]any) (any, error) {
	id := argStr(a, "skill_id")
	if id == "" {
		return map[string]any{}, nil
	}
	sk, err := LoadSkillForRun(t.DB, ss.Scope, t.runKey(conv), id)
	if err != nil {
		return nil, fmt.Errorf("skill %s: %w", id, err)
	}
	return sk.Ref(), nil
}

var reportClaimSchema = map[string]any{"type": "object", "required": []string{"text", "kind", "citations"}, "properties": map[string]any{
	"text": tStr, "kind": map[string]any{"type": "string", "enum": []string{"source_statement", "cross_source_verified", "model_inference"}},
	"citations": map[string]any{"type": "array", "items": map[string]any{"type": "object", "required": []string{"evidence_id", "revision"},
		"properties": map[string]any{"evidence_id": tStr, "revision": tInt, "chunk_index": tInt}}}}}

var materialScopeSchema = map[string]any{"type": "object", "properties": map[string]any{"document_ids": tStrs, "source_ids": tStrs, "from": tStr, "to": tStr}}

var p3Tools = []ToolSpec{
	{Name: "intel_load_skill", Description: "Load an installed analysis skill (method, steps, references, output schema). The version is frozen for this run.",
		Class: "read", Scope: "intel.documents.read", Timeout: 5 * time.Second, OutputCap: 12000,
		Parameters: obj([]string{"skill_id"}, map[string]any{"skill_id": tStr}),
		local: func(t Tools, conv model.FlowAgentConversation, ss SubjectScope, a map[string]any) (any, error) {
			return LoadSkillForRun(t.DB, ss.Scope, t.runKey(conv), argStr(a, "skill_id"))
		}},
	{Name: "intel_start_research", Description: "Start a child research task (single depth, at most 3 per run) over a fixed evidence scope. Returns claims with evidence ids and coverage.",
		Class: "write", Scope: "intel.research", Timeout: 60 * time.Second, OutputCap: 16000,
		Parameters: obj([]string{"question"}, map[string]any{"question": tStr, "source_ids": tStrs, "document_ids": tStrs, "from": tStr, "to": tStr, "skill_id": tStr}),
		prepare: func(t Tools, _ context.Context, conv model.FlowAgentConversation, ss SubjectScope, _ string, a map[string]any) (map[string]any, error) {
			skill, err := skillRefFor(t, conv, ss, a)
			if err != nil {
				return nil, err
			}
			scope := map[string]any{}
			for _, k := range []string{"source_ids", "document_ids", "from", "to"} {
				if v, ok := a[k]; ok {
					scope[k] = v
				}
			}
			return map[string]any{"parent_run_id": t.runKey(conv), "question": a["question"], "evidence_scope": scope, "skill": skill}, nil
		},
		route: func(a map[string]any) (string, string, any) { return http.MethodPost, "/v1/research-runs", a }},
	{Name: "intel_save_report", Description: "Save a report with claims and citations. Every non-inference claim must cite evidence (evidence_id + revision) inside material_scope. Claim kinds: source_statement | cross_source_verified (≥2 sources) | model_inference.",
		Class: "write", Scope: "intel.reports.write", Timeout: 20 * time.Second, OutputCap: 8000,
		Parameters: obj([]string{"title", "claims", "material_scope"}, map[string]any{"title": tStr, "body": tStr, "skill_id": tStr, "research_run_id": tStr,
			"claims": map[string]any{"type": "array", "items": reportClaimSchema}, "material_scope": materialScopeSchema}),
		prepare: func(t Tools, ctx context.Context, conv model.FlowAgentConversation, ss SubjectScope, userID string, a map[string]any) (map[string]any, error) {
			var claims []ReportClaim
			var scope MaterialScope
			if err := decodeInto(a["claims"], &claims); err != nil {
				return nil, fmt.Errorf("%w: claims: %v", ErrToolArgs, err)
			}
			if err := decodeInto(a["material_scope"], &scope); err != nil {
				return nil, fmt.Errorf("%w: material_scope: %v", ErrToolArgs, err)
			}
			ids := []string{}
			for _, c := range claims {
				for _, ct := range c.Citations {
					ids = append(ids, ct.EvidenceID)
				}
			}
			resolved, err := t.resolveEvidence(ctx, ss, userID, ids)
			if err != nil {
				return nil, err
			}
			truncated, err := ValidateCitations(claims, scope, resolved)
			if err != nil {
				return nil, err
			}
			skill, err := skillRefFor(t, conv, ss, a)
			if err != nil {
				return nil, err
			}
			return map[string]any{"title": a["title"], "body": a["body"], "claims": claims, "material_scope": scope, "skill": skill,
				"research_run_id": a["research_run_id"], "coverage": map[string]any{"truncated_citations": truncated, "truncated": len(truncated) > 0}}, nil
		},
		route: func(a map[string]any) (string, string, any) { return http.MethodPost, "/v1/reports", a }},
	{Name: "intel_preview_schedule", Description: "Preview a periodic brief schedule (query, sources, 5-field cron, IANA timezone, window hours). Returns next run times; does not save.",
		Class: "read", Scope: "intel.sources.read", Timeout: 10 * time.Second, OutputCap: 8000,
		Parameters: obj([]string{"query", "source_ids", "cron", "timezone"}, map[string]any{"name": tStr, "query": tStr, "source_ids": tStrs, "cron": tStr, "timezone": tStr, "window_hours": tInt, "skill_id": tStr}),
		prepare: func(t Tools, _ context.Context, conv model.FlowAgentConversation, ss SubjectScope, _ string, a map[string]any) (map[string]any, error) {
			skill, err := skillRefFor(t, conv, ss, a)
			if err != nil {
				return nil, err
			}
			out := map[string]any{"skill": skill}
			for k, v := range a {
				if k != "skill_id" {
					out[k] = v
				}
			}
			return out, nil
		},
		route: func(a map[string]any) (string, string, any) { return http.MethodPost, "/v1/brief-schedules/preview", a }},
	{Name: "intel_apply_schedule", Description: "Save a previewed brief schedule. Requires user approval; runs only after approval.",
		Class: "write", Scope: "intel.schedules.write", NeedsApproval: true, Timeout: 15 * time.Second, OutputCap: 8000,
		Parameters: obj([]string{"preview_id"}, map[string]any{"preview_id": tStr}),
		route:      func(a map[string]any) (string, string, any) { return http.MethodPost, "/v1/brief-schedules", a }},
}

func (t Tools) resolveEvidence(ctx context.Context, ss SubjectScope, userID string, ids []string) ([]ResolvedEvidence, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	body, _ := json.Marshal(map[string]any{"evidence_ids": ids})
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	status, out, err := t.Engine.Do(cctx, ss.Scope, Actor{Type: "user", ID: userID}, http.MethodPost, "/v1/evidence/resolve", "", body)
	if err != nil || status >= 400 {
		return nil, fmt.Errorf("evidence lookup failed (status %d)", status)
	}
	var env struct {
		Data []ResolvedEvidence `json:"data"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, err
	}
	return env.Data, nil
}

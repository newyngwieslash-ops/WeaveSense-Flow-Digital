package edumath

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"yingce/backend/internal/model"
)

// TestSixFamiliesThroughPlatform drives the five families added after G1
// through the real platform API and the real math service: create → confirm →
// analyze (verified, layered evidence) → lesson → student projection accepted by
// the platform answer guard, and a negative case returning a typed error.
func TestSixFamiliesThroughPlatform(t *testing.T) {
	h := newHarness(t, startMathService(t))
	tenant := h.setupTeacher()
	cases := []struct {
		fid    string
		params map[string]any
		extra  map[string]any
		answer string
	}{
		{"SOLID-VOL-01", map[string]any{"l": "3", "w": "20", "h": "5"}, map[string]any{"units": map[string]any{"l": "cm", "w": "mm", "h": "cm"}, "target_unit": "cm"}, "30"},
		{"SOLID-ANGLE-01", map[string]any{"e": "2"}, nil, "sqrt(3)/3"},
		{"CONIC-CHORD-01", map[string]any{"a": "2", "b": "sqrt(3)"}, nil, "[3, 4]"},
		{"CONIC-SLOPE-01", map[string]any{"a": "2", "b": "sqrt(3)"}, nil, "-3/4"},
		{"CONIC-PARABOLA-01", map[string]any{"p": "2"}, nil, "-3"},
	}
	for _, c := range cases {
		st := map[string]any{"family_id": c.fid, "input_state": "sufficient", "params": c.params}
		for k, v := range c.extra {
			st[k] = v
		}
		code, out, raw, _ := h.call("u_alice", "POST", "/api/edu/problems", map[string]any{"familyId": c.fid, "title": c.fid, "structured": st})
		if code != 201 {
			t.Fatalf("%s create: %d %s", c.fid, code, raw)
		}
		rev := data(out)["revision"].(map[string]any)
		revID := rev["id"].(string)
		if code, _, raw, _ := h.call("u_alice", "POST", "/api/edu/problem-revisions/"+revID+"/confirm", map[string]any{"inputHash": rev["inputHash"]}); code != 200 {
			t.Fatalf("%s confirm: %d %s", c.fid, code, raw)
		}
		code, out, raw, _ = h.call("u_alice", "POST", "/api/edu/problem-revisions/"+revID+"/analyze", map[string]any{"idempotencyKey": "fam-a-" + c.fid})
		if code != 202 {
			t.Fatalf("%s analyze: %d %s", c.fid, code, raw)
		}
		job := h.waitJob("u_alice", data(out)["id"].(string))
		if job["status"] != "succeeded" {
			t.Fatalf("%s analysis: %v", c.fid, job)
		}
		var sol model.EduSolutionRevision
		h.db.First(&sol, "id = ?", job["solutionRevisionId"])
		if sol.MathState != "verified" || !strings.Contains(sol.EvidenceLevels, "exact_instance") {
			t.Fatalf("%s evidence: %+v", c.fid, sol)
		}
		if c.fid != "SOLID-VOL-01" && !strings.Contains(sol.EvidenceLevels, "symbolic") {
			t.Fatalf("%s should carry a symbolic domain identity: %s", c.fid, sol.EvidenceLevels)
		}
		code, out, raw, _ = h.call("u_alice", "POST", "/api/edu/solution-revisions/"+sol.ID+"/lesson", map[string]any{"idempotencyKey": "fam-l-" + c.fid})
		if code != 202 {
			t.Fatalf("%s lesson: %d %s", c.fid, code, raw)
		}
		lj := h.waitJob("u_alice", data(out)["id"].(string))
		if lj["status"] != "succeeded" {
			t.Fatalf("%s lesson job: %v", c.fid, lj)
		}
		var l model.EduLessonRevision
		h.db.First(&l, "id = ?", lj["lessonRevisionId"])
		var sa, ta model.EduArtifact
		h.db.First(&sa, "id = ?", l.StudentArtifact)
		h.db.First(&ta, "id = ?", l.LessonArtifact)
		ctx := context.Background()
		stu, _, err := h.svc.Client.Artifact(ctx, tenant, sa.ServiceArtifactID, sa.SHA256)
		if err != nil {
			t.Fatal(err)
		}
		teacher, _, err := h.svc.Client.Artifact(ctx, tenant, ta.ServiceArtifactID, ta.SHA256)
		if err != nil {
			t.Fatal(err)
		}
		var proj map[string]any
		var tl struct {
			Answer struct {
				Exact string `json:"exact"`
			} `json:"answer"`
		}
		_ = json.Unmarshal(stu, &proj)
		_ = json.Unmarshal(teacher, &tl)
		if StudentLeak(proj, teacher) {
			t.Fatalf("%s student projection rejected by guard", c.fid)
		}
		if tl.Answer.Exact != c.answer {
			t.Fatalf("%s answer %q, want %q", c.fid, tl.Answer.Exact, c.answer)
		}
	}
	// Out-of-catalog configuration: typed rejection, nothing published.
	st := map[string]any{"family_id": "CONIC-CHORD-01", "input_state": "sufficient", "params": map[string]any{"a": "2", "b": "2"}}
	_, out, _, _ := h.call("u_alice", "POST", "/api/edu/problems", map[string]any{"familyId": "CONIC-CHORD-01", "title": "circle", "structured": st})
	rev := data(out)["revision"].(map[string]any)
	h.call("u_alice", "POST", "/api/edu/problem-revisions/"+rev["id"].(string)+"/confirm", map[string]any{"inputHash": rev["inputHash"]})
	_, out, _, _ = h.call("u_alice", "POST", "/api/edu/problem-revisions/"+rev["id"].(string)+"/analyze", map[string]any{"idempotencyKey": "fam-circle"})
	job := h.waitJob("u_alice", data(out)["id"].(string))
	if job["status"] != "failed" || !strings.Contains(asJSON(job), "CONDITION_CONFLICT") {
		t.Fatalf("a = b must fail with CONDITION_CONFLICT: %v", job)
	}
}

func asJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

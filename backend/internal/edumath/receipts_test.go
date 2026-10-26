package edumath

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"yingce/backend/internal/model"
)

// TestReceiptContract (DEV-053): the job receipt is decoded strictly. Every
// deviation from edu.math.job/1 fails closed with ErrContract.
func TestReceiptContract(t *testing.T) {
	good := func() map[string]any {
		v := sv(svcID(1), "running", 2, StageView{Name: "validate_input", Status: "succeeded", ComputeMS: 3}, StageView{Name: "solve", Status: "running"})
		v.Stage, v.UsageState, v.RequestID = "solve", "accruing", "emj_x"
		v.ResultRefs = []ArtifactRef{{ArtifactID: "ma_" + strings.Repeat("0", 32), Kind: "figure", Audience: "teacher", MediaType: "image/png",
			SHA256: "sha256:" + strings.Repeat("cd", 32), Size: 10}}
		raw, _ := json.Marshal(v)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return m
	}
	enc := func(m map[string]any) []byte { b, _ := json.Marshal(m); return b }
	if _, err := decodeJob(200, enc(good())); err != nil {
		t.Fatalf("valid receipt (non-text artifact) rejected: %v", err)
	}
	stage := func(m map[string]any, i int) map[string]any { return m["stages"].([]any)[i].(map[string]any) }
	cases := map[string]func(m map[string]any) []byte{
		"unknown field":         func(m map[string]any) []byte { m["answer"] = "42"; return enc(m) },
		"trailing data":         func(m map[string]any) []byte { return append(enc(m), []byte(`{"seq":9}`)...) },
		"truncated":             func(m map[string]any) []byte { b := enc(m); return b[:len(b)-7] },
		"empty body":            func(m map[string]any) []byte { return nil },
		"wrong schema":          func(m map[string]any) []byte { m["schema_version"] = "edu.math.job/2"; return enc(m) },
		"artifact id as job id": func(m map[string]any) []byte { m["job_id"] = "ma_" + strings.Repeat("1", 32); return enc(m) },
		"unknown kind":          func(m map[string]any) []byte { m["kind"] = "shell"; return enc(m) },
		"missing input hash":    func(m map[string]any) []byte { delete(m, "input_hash"); return enc(m) },
		"seq zero":              func(m map[string]any) []byte { m["seq"] = 0; return enc(m) },
		"progress above 1":      func(m map[string]any) []byte { m["progress"] = 1.5; return enc(m) },
		"missing stage":         func(m map[string]any) []byte { m["stages"] = m["stages"].([]any)[:2]; return enc(m) },
		"reordered stages": func(m map[string]any) []byte {
			s := m["stages"].([]any)
			s[0], s[1] = s[1], s[0]
			return enc(m)
		},
		"bad stage status":       func(m map[string]any) []byte { stage(m, 2)["status"] = "done"; return enc(m) },
		"negative compute":       func(m map[string]any) []byte { stage(m, 0)["compute_ms"] = -1; return enc(m) },
		"unknown current stage":  func(m map[string]any) []byte { m["stage"] = "render"; return enc(m) },
		"succeeded with pending": func(m map[string]any) []byte { m["status"] = "succeeded"; return enc(m) },
		"failed without error":   func(m map[string]any) []byte { m["status"] = "failed"; return enc(m) },
		"usage for running stage": func(m map[string]any) []byte {
			m["usage"] = []any{map[string]any{"stage": "solve", "unit": "compute_ms", "quantity": 1}}
			return enc(m)
		},
		"usage for unknown stage": func(m map[string]any) []byte {
			m["usage"] = []any{map[string]any{"stage": "x", "unit": "compute_ms", "quantity": 1}}
			return enc(m)
		},
		"duplicate usage": func(m map[string]any) []byte {
			m["usage"] = append(m["usage"].([]any), m["usage"].([]any)[0])
			return enc(m)
		},
		"usage unit": func(m map[string]any) []byte {
			m["usage"].([]any)[0].(map[string]any)["unit"] = "tokens"
			return enc(m)
		},
		"artifact media type": func(m map[string]any) []byte {
			m["result_refs"].([]any)[0].(map[string]any)["media_type"] = "application/x-sh"
			return enc(m)
		},
		"artifact size zero": func(m map[string]any) []byte { m["result_refs"].([]any)[0].(map[string]any)["size"] = 0; return enc(m) },
		"artifact size over limit": func(m map[string]any) []byte {
			m["result_refs"].([]any)[0].(map[string]any)["size"] = MaxArtifactBytes + 1
			return enc(m)
		},
		"artifact bad hash": func(m map[string]any) []byte {
			m["result_refs"].([]any)[0].(map[string]any)["sha256"] = "sha256:abc"
			return enc(m)
		},
		"artifact no kind": func(m map[string]any) []byte {
			m["result_refs"].([]any)[0].(map[string]any)["kind"] = ""
			return enc(m)
		},
		"duplicate artifact": func(m map[string]any) []byte {
			m["result_refs"] = append(m["result_refs"].([]any), m["result_refs"].([]any)[0])
			return enc(m)
		},
	}
	for name, mutate := range cases {
		if _, err := decodeJob(200, mutate(good())); !errors.Is(err, ErrContract) {
			t.Errorf("%s: want ErrContract, got %v", name, err)
		}
	}
}

// TestReceiptFailClosed (DEV-053, DEV-054): invalid, oversized and truncated
// receipts are never applied and are recorded on the job; a later valid receipt
// supersedes the note. A succeeded job whose artifacts fail verification
// (truncated body, wrong media type) fails closed with RECEIPT_INVALID, creates
// no solution revision, charges the stages that actually ran, and lists its
// artifacts as partial metadata without content.
func TestReceiptFailClosed(t *testing.T) {
	sc, base := newScripted(t)
	h := newHarness(t, base)
	h.setupTeacher()
	rev := h.confirmedRevision("2", "1")
	get := func(id string) map[string]any {
		t.Helper()
		code, out, raw, _ := h.call("u_alice", "GET", "/api/edu/jobs/"+id, nil)
		if code != 200 {
			t.Fatalf("get: %d %s", code, raw)
		}
		return data(out)
	}
	start := func(key string) string {
		t.Helper()
		code, out, raw, _ := h.call("u_alice", "POST", "/api/edu/problem-revisions/"+rev+"/analyze", map[string]any{"idempotencyKey": key})
		if code >= 300 {
			t.Fatalf("start: %d %s", code, raw)
		}
		return data(out)["id"].(string)
	}

	// 1) Invalid receipts while running.
	running := sv(svcID(11), "running", 1, StageView{Name: "validate_input", Status: "succeeded", ComputeMS: 5})
	sc.set(func(s *scriptedService) { s.scripted, s.view = true, running })
	id := start("receipt-running")
	good, _ := json.Marshal(sv(svcID(11), "running", 2, StageView{Name: "validate_input", Status: "succeeded", ComputeMS: 5}))
	pad := append([]byte(`{"pad":"`), []byte(strings.Repeat("x", MaxJobReceiptBytes))...)
	for name, raw := range map[string][]byte{"truncated": good[:len(good)/2], "unknown field": append(good[:len(good)-1], []byte(`,"answer":"2"}`)...),
		"oversized": append(pad, '"', '}')} {
		sc.set(func(s *scriptedService) { s.raw = raw })
		g := get(id)
		e, _ := g["error"].(map[string]any)
		if g["status"] != "running" || g["seq"].(float64) != 1 || e["code"] != "RECEIPT_INVALID" {
			t.Fatalf("%s receipt must be noted, not applied: %v", name, g)
		}
	}
	sc.set(func(s *scriptedService) {
		s.raw = nil
		s.view = sv(svcID(11), "running", 2, StageView{Name: "validate_input", Status: "succeeded", ComputeMS: 5})
	})
	if g := get(id); g["seq"].(float64) != 2 || g["error"] != nil {
		t.Fatalf("valid receipt must apply and clear the note: %v", g)
	}

	// 2) Real jobs whose artifacts are tampered in transit.
	sc.set(func(s *scriptedService) { s.scripted, s.raw = false, nil })
	for _, mode := range []string{"truncate", "media"} {
		sc.set(func(s *scriptedService) { s.artMode = mode })
		jid := start("receipt-art-" + mode)
		d := h.waitJob("u_alice", jid)
		e, _ := d["error"].(map[string]any)
		if d["status"] != "failed" || e["code"] != "RECEIPT_INVALID" || d["settlement"] != "settled" || d["solutionRevisionId"] != nil {
			t.Fatalf("%s: tampered artifact must fail closed: %v", mode, d)
		}
		var n int64
		h.db.Model(&model.EduSolutionRevision{}).Where("job_id = ?", jid).Count(&n)
		if n != 0 {
			t.Fatalf("%s: no revision may come from an unverified artifact", mode)
		}
		var runs []model.EduStageRun
		h.db.Where("job_id = ?", jid).Find(&runs)
		if len(runs) != 3 {
			t.Fatalf("%s: the three stages that ran stay charged once: %d", mode, len(runs))
		}
		arts, _ := d["artifacts"].([]any)
		if len(arts) < 2 {
			t.Fatalf("%s: partial artifacts must be listed: %v", mode, d)
		}
		for _, x := range arts {
			a := x.(map[string]any)
			if a["partial"] != true || a["sha256"] == nil || a["content"] != nil || a["url"] != nil {
				t.Fatalf("%s: partial artifact metadata only: %v", mode, a)
			}
		}
		// Repeated polls of the terminal job change nothing.
		for i := 0; i < 5; i++ {
			if g := get(jid); g["status"] != "failed" || g["settlement"] != "settled" {
				t.Fatalf("%s: terminal job changed: %v", mode, g)
			}
		}
	}

	// 3) Untampered control: the same flow succeeds.
	sc.set(func(s *scriptedService) { s.artMode = "" })
	if d := h.waitJob("u_alice", start("receipt-ok")); d["status"] != "succeeded" || d["solutionRevisionId"] == nil {
		t.Fatalf("control job: %v", d)
	}
}

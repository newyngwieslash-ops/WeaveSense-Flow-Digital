package flow

import (
	"testing"

	"yingce/backend/internal/model"
)

// §9.4: model synthesis drafts pass through the host Reviewer.
func TestReviewerRejectsUnsupportedAndFlagsConflictAndInference(t *testing.T) {
	ev := []ResolvedEvidence{
		{EvidenceID: "e1", SourceID: "s1", Revision: 1, Quote: "锂价本月上涨10%，供应偏紧"},
		{EvidenceID: "e2", SourceID: "s2", Revision: 1, Quote: "lithium price up 12% on tight supply"},
	}
	claims := []ReportClaim{
		{Text: "锂价本月上涨", Kind: "source_statement", Citations: []Citation{{EvidenceID: "e1", Revision: 1}}},
		{Text: "gold reserves doubled worldwide", Kind: "source_statement", Citations: []Citation{{EvidenceID: "e2", Revision: 1}}},
		{Text: "lithium price 锂价 rising", Kind: "cross_source_verified", Citations: []Citation{{EvidenceID: "e1", Revision: 1}, {EvidenceID: "e2", Revision: 1}}},
		{Text: "prices may keep rising", Kind: "model_inference"},
	}
	got, r := ReviewClaims(claims, ev)
	if len(got) != 3 || r.Verdict != "rejected_claims" || len(r.Rejected) != 1 || r.Rejected[0].Idx != 1 {
		t.Fatalf("review: %+v", r)
	}
	flags := map[int][]string{}
	for _, f := range r.Flagged {
		flags[f.Idx] = f.Flags
	}
	if len(flags[2]) == 0 || flags[2][0] != "conflict" || flags[3][0] != "uncertain" {
		t.Fatalf("flags: %+v", flags)
	}
	syn, err := FinishSynthesis(modelTaskWithResult(`{"body":"b","claims":[{"text":"gold reserves doubled worldwide","kind":"source_statement","citations":[{"evidence_id":"e2","revision":1}]}]}`),
		SynthesisRequest{Evidence: ev})
	if err != nil || len(syn.Claims) != 0 || syn.Review == nil || len(syn.Dropped) != 1 {
		t.Fatalf("synthesis must drop reviewer-rejected claims: %+v %v", syn, err)
	}
}

func modelTaskWithResult(r string) model.FlowModelTask { return model.FlowModelTask{Result: r} }

package flow

import (
	"regexp"
	"strings"
)

// PRD §9.4: the producer (model synthesis) writes a draft; a fixed host
// Reviewer step checks it before use. Citation validity (existence, revision,
// scope, kind rules) is ValidateCitations; the Reviewer additionally checks
// that the cited quotes support the claim's content, flags conflicts between
// sources and marks model inferences as uncertain. Same rules as the engine's
// Reports.review (services/loom-engine/flow/reports.ts).

// ReviewResult is the Reviewer's verdict on one draft.
type ReviewResult struct {
	Reviewer string         `json:"reviewer"`
	Verdict  string         `json:"verdict"` // passed | flagged | rejected_claims
	Checked  int            `json:"checked"`
	Accepted int            `json:"accepted"`
	Rejected []ReviewReject `json:"rejected,omitempty"`
	Flagged  []ReviewFlag   `json:"flagged,omitempty"`
}

type ReviewReject struct {
	Idx    int    `json:"idx"`
	Text   string `json:"text"`
	Reason string `json:"reason"`
}

type ReviewFlag struct {
	Idx   int      `json:"idx"`
	Flags []string `json:"flags"`
}

// SupportThreshold: share of a model claim's terms that must appear in its
// cited quotes (model drafts paraphrase, so lower than the extractive 0.5).
const SupportThreshold = 0.3

var (
	reLatin = regexp.MustCompile(`[a-z][a-z0-9-]{2,}`)
	reCJK   = regexp.MustCompile(`[\p{Han}]+`)
	reNum   = regexp.MustCompile(`\d+(?:\.\d+)?`)
	reFact  = regexp.MustCompile(`(?i)(-?\d+(?:\.\d+)?)\s*(%|％|percent|亿元|万元|亿美元|美元|万吨|吨|gwh|mwh|gw|mw|万辆|辆)`)
	stop    = map[string]bool{"the": true, "and": true, "for": true, "with": true, "that": true, "this": true, "from": true, "are": true, "was": true, "has": true, "have": true}
)

func claimTerms(text string) map[string]bool {
	t := strings.ToLower(text)
	out := map[string]bool{}
	for _, w := range reLatin.FindAllString(t, -1) {
		if !stop[w] {
			out[w] = true
		}
	}
	for _, run := range reCJK.FindAllString(t, -1) {
		r := []rune(run)
		for i := 0; i+1 < len(r); i++ {
			out[string(r[i:i+2])] = true
		}
	}
	for _, n := range reNum.FindAllString(t, -1) {
		out[n] = true
	}
	return out
}

// SupportRatio is the fraction of the claim's terms found in the quotes.
func SupportRatio(claim string, quotes []string) float64 {
	c := claimTerms(claim)
	if len(c) == 0 {
		return 0
	}
	q := claimTerms(strings.Join(quotes, " "))
	hit := 0
	for k := range c {
		if q[k] {
			hit++
		}
	}
	return float64(hit) / float64(len(c))
}

type fact struct{ value, unit string }

func facts(s string) []fact {
	out := []fact{}
	for _, m := range reFact.FindAllStringSubmatch(s, 10) {
		u := strings.ToLower(strings.ReplaceAll(m[2], "％", "%"))
		if u == "percent" {
			u = "%"
		}
		out = append(out, fact{m[1], u})
	}
	return out
}

// ReviewClaims runs the Reviewer over citation-valid claims.
func ReviewClaims(claims []ReportClaim, evidence []ResolvedEvidence) ([]ReportClaim, ReviewResult) {
	ev := map[string]ResolvedEvidence{}
	for _, e := range evidence {
		ev[e.EvidenceID] = e
	}
	res := ReviewResult{Reviewer: "host_reviewer_v1", Checked: len(claims)}
	var accepted []ReportClaim
	for i, c := range claims {
		var quotes []string
		srcs := map[string]bool{}
		var cited []ResolvedEvidence
		for _, x := range c.Citations {
			e := ev[x.EvidenceID]
			quotes = append(quotes, e.Quote)
			srcs[e.SourceID] = true
			cited = append(cited, e)
		}
		reason := ""
		switch c.Kind {
		case "source_statement":
			if SupportRatio(c.Text, quotes) < SupportThreshold {
				reason = "claim not supported by cited evidence"
			}
		case "cross_source_verified":
			for _, q := range quotes {
				if SupportRatio(c.Text, []string{q}) == 0 {
					reason = "a cited source does not support the claim"
				}
			}
		}
		if reason != "" {
			res.Rejected = append(res.Rejected, ReviewReject{Idx: i, Text: truncRunes(c.Text, 200), Reason: reason})
			continue
		}
		var flags []string
		if c.Kind == "model_inference" {
			flags = append(flags, "uncertain")
		}
	outer:
		for a := 0; a < len(cited); a++ {
			for b := a + 1; b < len(cited); b++ {
				if cited[a].SourceID == cited[b].SourceID {
					continue
				}
				for _, fa := range facts(cited[a].Quote) {
					for _, fb := range facts(cited[b].Quote) {
						if fa.unit == fb.unit && fa.value != fb.value {
							flags = append(flags, "conflict")
							break outer
						}
					}
				}
			}
		}
		if len(flags) > 0 {
			res.Flagged = append(res.Flagged, ReviewFlag{Idx: i, Flags: flags})
		}
		accepted = append(accepted, c)
	}
	res.Accepted = len(accepted)
	switch {
	case len(res.Rejected) > 0:
		res.Verdict = "rejected_claims"
	case len(res.Flagged) > 0:
		res.Verdict = "flagged"
	default:
		res.Verdict = "passed"
	}
	return accepted, res
}

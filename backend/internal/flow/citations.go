package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ResolvedEvidence is what the Engine reports for an evidence id visible to the caller.
type ResolvedEvidence struct {
	EvidenceID string `json:"evidence_id"`
	DocumentID string `json:"document_id"`
	SourceID   string `json:"source_id"`
	Revision   int    `json:"revision"`
	ChunkIndex int    `json:"chunk_index"`
	Truncated  bool   `json:"truncated"`
	Title      string `json:"title,omitempty"`
	Quote      string `json:"quote,omitempty"`
}

// Citation as submitted by the model.
type Citation struct {
	EvidenceID string `json:"evidence_id"`
	Revision   int    `json:"revision"`
	ChunkIndex *int   `json:"chunk_index,omitempty"`
}

type ReportClaim struct {
	Text      string     `json:"text"`
	Kind      string     `json:"kind"`
	Citations []Citation `json:"citations"`
}

// MaterialScope is the saved set of material a report may cite.
type MaterialScope struct {
	DocumentIDs []string `json:"document_ids,omitempty"`
	SourceIDs   []string `json:"source_ids,omitempty"`
	From        string   `json:"from,omitempty"`
	To          string   `json:"to,omitempty"`
}

var ErrCitation = errors.New("flow: citation rejected")

// ValidateCitations checks every citation against evidence resolved in the
// caller's scope. It rejects unknown/foreign evidence, wrong revision or
// chunk, and documents outside the report's material scope. Truncated
// evidence is allowed but returned so the report can disclose it.
func ValidateCitations(claims []ReportClaim, scope MaterialScope, resolved []ResolvedEvidence) (truncated []string, err error) {
	if len(scope.DocumentIDs) == 0 && len(scope.SourceIDs) == 0 {
		return nil, fmt.Errorf("%w: material_scope must list document_ids or source_ids", ErrCitation)
	}
	byID := map[string]ResolvedEvidence{}
	for _, r := range resolved {
		byID[r.EvidenceID] = r
	}
	inScope := func(r ResolvedEvidence) bool {
		for _, d := range scope.DocumentIDs {
			if d == r.DocumentID {
				return true
			}
		}
		for _, s := range scope.SourceIDs {
			if s == r.SourceID {
				return true
			}
		}
		return false
	}
	var problems []string
	for i, c := range claims {
		switch c.Kind {
		case "source_statement", "cross_source_verified", "model_inference":
		default:
			problems = append(problems, fmt.Sprintf("claim %d: invalid kind %q", i, c.Kind))
			continue
		}
		if strings.TrimSpace(c.Text) == "" {
			problems = append(problems, fmt.Sprintf("claim %d: empty text", i))
		}
		if c.Kind != "model_inference" && len(c.Citations) == 0 {
			problems = append(problems, fmt.Sprintf("claim %d: %s needs at least one citation", i, c.Kind))
		}
		sources := map[string]bool{}
		for _, ct := range c.Citations {
			r, ok := byID[ct.EvidenceID]
			switch {
			case !ok:
				problems = append(problems, fmt.Sprintf("claim %d: evidence %s not found", i, ct.EvidenceID))
				continue
			case ct.Revision != r.Revision:
				problems = append(problems, fmt.Sprintf("claim %d: evidence %s is revision %d, cited %d", i, ct.EvidenceID, r.Revision, ct.Revision))
			case ct.ChunkIndex != nil && *ct.ChunkIndex != r.ChunkIndex:
				problems = append(problems, fmt.Sprintf("claim %d: evidence %s is chunk %d, cited %d", i, ct.EvidenceID, r.ChunkIndex, *ct.ChunkIndex))
			case !inScope(r):
				problems = append(problems, fmt.Sprintf("claim %d: evidence %s is outside the report material scope", i, ct.EvidenceID))
			}
			if r.Truncated {
				truncated = append(truncated, ct.EvidenceID)
			}
			sources[r.SourceID] = true
		}
		if c.Kind == "cross_source_verified" && len(sources) < 2 {
			problems = append(problems, fmt.Sprintf("claim %d: cross_source_verified needs evidence from ≥2 sources", i))
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrCitation, strings.Join(problems, "; "))
	}
	return truncated, nil
}

func decodeInto(v any, out any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

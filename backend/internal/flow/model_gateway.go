package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"yingce/backend/internal/model"
)

// ModelGateway is the platform's single path to LLMs for flow executors
// (research synthesis, brief writing). Every call is budgeted by Billing and
// every returned citation is validated before use.
type ModelGateway interface {
	Complete(ctx context.Context, system, user string, maxTokens int) (text string, costMicro int64, err error)
}

// ErrModelUnknownOutcome means the request may have been processed (e.g. the
// connection dropped after sending); the reservation goes to "unknown".
var ErrModelUnknownOutcome = errors.New("flow: model call outcome unknown")

// OpenAICompatGateway calls an OpenAI-compatible /chat/completions endpoint
// (FLOW_MODEL_GATEWAY_URL / _KEY / _MODEL). Cost = tokens × FLOW_MODEL_MICRO_PER_1K_TOKENS / 1000.
type OpenAICompatGateway struct {
	URL, Key, Model string
	MicroPer1KTok   int64
	HTTP            *http.Client
}

func ModelGatewayFromEnv() *OpenAICompatGateway {
	u := strings.TrimRight(strings.TrimSpace(os.Getenv("FLOW_MODEL_GATEWAY_URL")), "/")
	if u == "" {
		return nil
	}
	price, _ := strconv.ParseInt(os.Getenv("FLOW_MODEL_MICRO_PER_1K_TOKENS"), 10, 64)
	if price <= 0 {
		price = 2000
	}
	return &OpenAICompatGateway{URL: u, Key: os.Getenv("FLOW_MODEL_GATEWAY_KEY"), Model: os.Getenv("FLOW_MODEL_GATEWAY_MODEL"), MicroPer1KTok: price}
}

func (g *OpenAICompatGateway) Complete(ctx context.Context, system, user string, maxTokens int) (string, int64, error) {
	body, _ := json.Marshal(map[string]any{"model": g.Model, "max_tokens": maxTokens, "temperature": 0.2,
		"response_format": map[string]any{"type": "json_object"},
		"messages":        []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.URL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if g.Key != "" {
		req.Header.Set("Authorization", "Bearer "+g.Key)
	}
	if k, _ := ctx.Value(modelIdemCtx{}).(string); k != "" {
		// Lets the gateway dedupe retries and answer status lookups (ModelStatus).
		req.Header.Set("Idempotency-Key", k)
	}
	hc := g.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 90 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		// Sent but no answer: the provider may have billed it.
		return "", 0, fmt.Errorf("%w: %v", ErrModelUnknownOutcome, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusTooManyRequests {
		return "", 0, fmt.Errorf("%w: upstream 429", ErrModelRetryable)
	}
	if resp.StatusCode >= 500 {
		return "", 0, fmt.Errorf("%w: upstream %d", ErrModelUnknownOutcome, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return "", 0, fmt.Errorf("model gateway %d", resp.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return "", 0, errors.New("model gateway: invalid response")
	}
	return out.Choices[0].Message.Content, out.Usage.TotalTokens * g.MicroPer1KTok / 1000, nil
}

// EvidenceForModel is what the model may cite.
type EvidenceForModel = ResolvedEvidence

type SynthesisRequest struct {
	TenantID    string
	ParentResID string // allocate from this reservation (child); "" = root reservation
	Key         string // idempotency key for the reservation
	Purpose     string // research | brief
	Question    string
	Evidence    []EvidenceForModel
	Scope       MaterialScope
	BudgetMicro int64
	MaxTokens   int
}

type Synthesis struct {
	Body          string        `json:"body"`
	Claims        []ReportClaim `json:"claims"`
	Dropped       []string      `json:"dropped_claims,omitempty"`
	ReservationID string        `json:"reservation_id"`
	TaskID        string        `json:"task_id"`
	CostMicro     int64         `json:"cost_micro"`
}

const synthesisSystem = `You write evidence-bound analysis. Output JSON only: {"body": string, "claims": [{"text": string, "kind": "source_statement"|"cross_source_verified"|"model_inference", "citations": [{"evidence_id": string, "revision": integer}]}]}.
Rules: cite only evidence ids given to you, with their revision. source_statement needs ≥1 citation; cross_source_verified needs citations from ≥2 different sources; anything you infer is model_inference. Never invent facts. Mention gaps (failed sources, truncation, conflicts) in body.`

// Synthesize reserves budget, calls the model, validates every citation and
// settles the reservation. Claims failing validation are dropped (reported).
func Synthesize(ctx context.Context, gw ModelGateway, b Billing, req SynthesisRequest) (Synthesis, error) {
	tasks, task, err := StartSynthesis(gw, b, req)
	if err != nil {
		return Synthesis{}, err
	}
	task, err = tasks.Wait(ctx, task.ID)
	if err != nil {
		return Synthesis{ReservationID: task.ReservationID, TaskID: task.ID}, err
	}
	return FinishSynthesis(task, req)
}

// StartSynthesis enqueues the model task (idempotent per tenant+key) and
// returns immediately; the model task worker executes it asynchronously.
func StartSynthesis(gw ModelGateway, b Billing, req SynthesisRequest) (ModelTasks, model.FlowModelTask, error) {
	tasks := ModelTasks{DB: b.DB, GW: gw, Now: b.Now, Status: ModelStatusFromEnv()}
	if gw == nil {
		return tasks, model.FlowModelTask{}, errors.New("flow: model gateway not configured")
	}
	if req.BudgetMicro <= 0 {
		req.BudgetMicro = 500_000
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = 1200
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Question / brief topic: %s\n\nEvidence:\n", req.Question)
	for _, e := range req.Evidence {
		fmt.Fprintf(&sb, "- id=%s revision=%d source=%s title=%q\n  quote: %s\n", e.EvidenceID, e.Revision, e.SourceID, e.Title, truncRunes(e.Quote, 600))
	}
	task, err := tasks.Enqueue(ModelTaskInput{TenantID: req.TenantID, ParentResID: req.ParentResID, Key: req.Key, Purpose: req.Purpose, RefID: req.Question,
		System: synthesisSystem, User: sb.String(), MaxTokens: req.MaxTokens, BudgetMicro: req.BudgetMicro})
	return tasks, task, err
}

// FinishSynthesis parses a succeeded task's output and validates every
// citation against the request's evidence and scope (invalid claims dropped).
func FinishSynthesis(task model.FlowModelTask, req SynthesisRequest) (Synthesis, error) {
	res := Synthesis{ReservationID: task.ReservationID, TaskID: task.ID, CostMicro: task.CostMicro}
	var parsed struct {
		Body   string        `json:"body"`
		Claims []ReportClaim `json:"claims"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(task.Result), "```json"), "```"))), &parsed); err != nil {
		return res, fmt.Errorf("model returned invalid JSON")
	}
	res.Body = parsed.Body
	for _, c := range parsed.Claims {
		if _, err := ValidateCitations([]ReportClaim{c}, req.Scope, req.Evidence); err != nil {
			res.Dropped = append(res.Dropped, truncRunes(c.Text, 200)+" — "+err.Error())
			continue
		}
		res.Claims = append(res.Claims, c)
	}
	return res, nil
}

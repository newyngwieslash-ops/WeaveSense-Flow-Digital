// Package edumath is the platform side of the education math workbench
// (docs/math-workbench): education roles and authorization, problem/solution/
// lesson revisions, math jobs with stage settlement on the platform ledger,
// private artifact retrieval, review and publication.
package edumath

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"yingce/backend/internal/model"
)

// RequestSchema / JobSchema are the frozen contract versions (docs/math-workbench/03-接口契约.md).
const (
	RequestSchema = "edu.math.request/1"
	JobSchema     = "edu.math.job/1"
)

var (
	// ErrServiceSent: the request may have reached the service; outcome unknown → reconcile, never blind retry.
	ErrServiceSent = errors.New("edumath: math service outcome unknown")
	ErrContract    = errors.New("edumath: math service response violates contract")
	serviceIDRe    = regexp.MustCompile(`^m[ja]_[0-9a-f]{32}$`)
	sha256Re       = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// Receipt limits (DEV-053). A response larger than its limit is refused as a
// whole: it is never cut to size and parsed.
const (
	MaxJobReceiptBytes = 1 << 20
	MaxArtifactBytes   = 16 << 20
)

// jobStages mirrors the service pipeline (services/math-service/mathsvc/pipeline.py).
// A receipt must list exactly these stages, in order.
var jobStages = map[string][]string{
	"analyze":      {"validate_input", "solve", "verify"},
	"lesson_build": {"compose_lesson", "project_student", "render_preview"},
	"variant_set":  {"generate_variants", "verify_variants"},
	"lesson_edit":  {"apply_edits", "project_student", "render_preview"},
}

var stageStatuses = map[string]bool{"pending": true, "running": true, "succeeded": true, "failed": true, "skipped": true, "cancelled": true}

// artifactMedia is the allowlist of artifact media types, text and non-text.
var artifactMedia = map[string]bool{"application/json": true, "text/html; charset=utf-8": true, "image/svg+xml": true, "image/png": true,
	"audio/mpeg": true, "video/mp4": true, "text/vtt; charset=utf-8": true}

// ServiceError is a structured error returned by the math service.
type ServiceError struct {
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	Retryable  bool           `json:"retryable"`
	NextAction string         `json:"next_action"`
	Details    map[string]any `json:"details,omitempty"`
}

func (e *ServiceError) Error() string { return e.Code + ": " + e.Message }

type ArtifactRef struct {
	ArtifactID string `json:"artifact_id"`
	Kind       string `json:"kind"`
	Audience   string `json:"audience"`
	MediaType  string `json:"media_type"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
}

type StageView struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	ComputeMS int64  `json:"compute_ms"`
	Error     string `json:"error,omitempty"`
}

type UsageLine struct {
	Stage    string `json:"stage"`
	Unit     string `json:"unit"`
	Quantity int64  `json:"quantity"`
}

// JobView is the service's job response (contract edu.math.job/1).
type JobView struct {
	SchemaVersion  string        `json:"schema_version"`
	RequestID      string        `json:"request_id,omitempty"`
	TraceID        string        `json:"trace_id,omitempty"`
	UsageState     string        `json:"usage_state,omitempty"`
	CatalogVersion string        `json:"catalog_version,omitempty"`
	JobID          string        `json:"job_id"`
	OperationID    string        `json:"operation_id"`
	Kind           string        `json:"kind"`
	Status         string        `json:"status"`
	Stage          string        `json:"stage"`
	Seq            int64         `json:"seq"`
	Progress       float64       `json:"progress"`
	Stages         []StageView   `json:"stages"`
	InputHash      string        `json:"input_hash"`
	ResultRefs     []ArtifactRef `json:"result_refs"`
	Usage          []UsageLine   `json:"usage"`
	Warnings       []any         `json:"warnings"`
	Error          *ServiceError `json:"error"`
}

var jobStatuses = map[string]bool{"queued": true, "running": true, "succeeded": true, "failed": true, "cancelled": true}

// validate enforces the edu.math.job/1 receipt contract (DEV-053): every
// required field, the exact stage list of the job kind, usage tied to finished
// stages at most once each, artifact references with allowed media types and
// bounded sizes, and status consistency. Anything else fails closed.
func (j JobView) validate() error {
	bad := func(what string) error { return fmt.Errorf("%w: %s", ErrContract, what) }
	if j.SchemaVersion != JobSchema || !serviceIDRe.MatchString(j.JobID) || !strings.HasPrefix(j.JobID, "mj_") || !jobStatuses[j.Status] {
		return bad("schema/job/status")
	}
	names, ok := jobStages[j.Kind]
	if !ok {
		return bad("kind")
	}
	if !sha256Re.MatchString(j.InputHash) {
		return bad("input_hash")
	}
	if j.Seq < 1 || j.Progress < 0 || j.Progress > 1 {
		return bad("seq/progress")
	}
	if len(j.Stages) != len(names) {
		return bad("stage list")
	}
	stage := map[string]StageView{}
	for i, st := range j.Stages {
		if st.Name != names[i] || !stageStatuses[st.Status] || st.ComputeMS < 0 {
			return bad("stage " + st.Name)
		}
		stage[st.Name] = st
		if j.Status == "succeeded" && st.Status != "succeeded" && st.Status != "skipped" {
			return bad("succeeded job with unfinished stage")
		}
	}
	if j.Stage != "" && stage[j.Stage].Name == "" {
		return bad("current stage")
	}
	if j.Status == "failed" && j.Error == nil {
		return bad("failed job without error")
	}
	seen := map[string]bool{}
	for _, u := range j.Usage {
		st := stage[u.Stage]
		if u.Unit != "compute_ms" || u.Quantity < 0 || st.Name == "" || seen[u.Stage] || (st.Status != "succeeded" && st.Status != "failed") {
			return bad("usage")
		}
		seen[u.Stage] = true
	}
	ids := map[string]bool{}
	for _, r := range j.ResultRefs {
		if !serviceIDRe.MatchString(r.ArtifactID) || !strings.HasPrefix(r.ArtifactID, "ma_") || ids[r.ArtifactID] || r.Kind == "" ||
			(r.Audience != "teacher" && r.Audience != "student") || !sha256Re.MatchString(r.SHA256) || !artifactMedia[r.MediaType] ||
			r.Size <= 0 || r.Size > MaxArtifactBytes {
			return bad("artifact ref")
		}
		ids[r.ArtifactID] = true
	}
	return nil
}

// strictJSON decodes exactly one JSON value with no unknown fields and no
// trailing data (a truncated or concatenated body fails).
func strictJSON(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data after JSON value")
	}
	return nil
}

// Client calls the math service's internal REST API with a signed tenant context.
// It is an exact trusted adapter: one configured base URL, no redirects, and the
// tenant always comes from the platform scope (never from the request body).
type Client struct {
	BaseURL string
	Key     []byte
	HTTP    *http.Client
	Now     func() time.Time
}

func (c Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Configured reports whether a math service is configured.
func (c Client) Configured() bool { return c.BaseURL != "" && len(c.Key) >= 32 }

func (c Client) sign(req *http.Request, tenant, actor string, body []byte) {
	ctx, _ := json.Marshal(map[string]any{"tenant_id": tenant, "actor_id": actor, "aud": "math-service", "exp": c.now().Add(2 * time.Minute).Unix()})
	cb := base64.RawURLEncoding.EncodeToString(ctx)
	bh := sha256.Sum256(body)
	mac := hmac.New(sha256.New, c.Key)
	mac.Write([]byte(cb + "\n" + req.Method + "\n" + req.URL.RequestURI() + "\n" + hex.EncodeToString(bh[:])))
	req.Header.Set("X-Math-Context", cb)
	req.Header.Set("X-Math-Signature", hex.EncodeToString(mac.Sum(nil)))
}

// MCPToken mints a tenant-bound MCP bearer for the math service's /mcp endpoint.
func MCPToken(key []byte, tenant string, ttl time.Duration, now time.Time) string {
	ctx, _ := json.Marshal(map[string]any{"tenant_id": tenant, "aud": "math-service", "exp": now.Add(ttl).Unix()})
	cb := base64.RawURLEncoding.EncodeToString(ctx)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("mcp\n" + cb))
	return "v1." + cb + "." + hex.EncodeToString(mac.Sum(nil))
}

func (c Client) do(ctx context.Context, method, path, tenant, actor string, body any) (int, []byte, http.Header, error) {
	return c.doLimit(ctx, method, path, tenant, actor, body, MaxJobReceiptBytes)
}

func (c Client) doLimit(ctx context.Context, method, path, tenant, actor string, body any, limit int64) (int, []byte, http.Header, error) {
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, nil, err
	}
	if body == nil {
		req.Body = http.NoBody
	}
	req.Header.Set("Content-Type", "application/json")
	c.sign(req, tenant, actor, raw)
	resp, err := c.http().Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: %v", ErrServiceSent, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: %v", ErrServiceSent, err)
	}
	if int64(len(data)) > limit {
		return 0, nil, nil, fmt.Errorf("%w: response exceeds %d bytes", ErrContract, limit)
	}
	return resp.StatusCode, data, resp.Header, nil
}

func decodeJob(status int, data []byte) (JobView, error) {
	var j JobView
	if status >= 400 {
		var e struct {
			Error *ServiceError `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != nil {
			return j, e.Error
		}
		return j, fmt.Errorf("%w: http %d", ErrContract, status)
	}
	if err := strictJSON(data, &j); err != nil {
		return j, fmt.Errorf("%w: %v", ErrContract, err)
	}
	return j, j.validate()
}

// StartJob submits a job under a logical idempotency key.
func (c Client) StartJob(ctx context.Context, tenant, actor, kind, key, requestID string, payload map[string]any) (JobView, error) {
	st, data, _, err := c.do(ctx, http.MethodPost, "/v1/jobs", tenant, actor, map[string]any{
		"schema_version": RequestSchema, "kind": kind, "idempotency_key": key, "request_id": requestID, "trace_id": requestID, "payload": payload})
	if err != nil {
		return JobView{}, err
	}
	return decodeJob(st, data)
}

func (c Client) GetJob(ctx context.Context, tenant, jobID string) (JobView, error) {
	if !serviceIDRe.MatchString(jobID) {
		return JobView{}, ErrContract
	}
	st, data, _, err := c.do(ctx, http.MethodGet, "/v1/jobs/"+jobID, tenant, "platform", nil)
	if err != nil {
		return JobView{}, err
	}
	return decodeJob(st, data)
}

func (c Client) CancelJob(ctx context.Context, tenant, jobID string) (JobView, error) {
	if !serviceIDRe.MatchString(jobID) {
		return JobView{}, ErrContract
	}
	st, data, _, err := c.do(ctx, http.MethodPost, "/v1/jobs/"+jobID+"/cancel", tenant, "platform", map[string]any{})
	if err != nil {
		return JobView{}, err
	}
	return decodeJob(st, data)
}

// Reconcile asks whether a start call with this key created a job (never re-executes).
func (c Client) Reconcile(ctx context.Context, tenant, kind, key string) (bool, JobView, error) {
	st, data, _, err := c.do(ctx, http.MethodPost, "/v1/reconcile", tenant, "platform", map[string]any{"kind": kind, "idempotency_key": key})
	if err != nil {
		return false, JobView{}, err
	}
	if st != http.StatusOK {
		return false, JobView{}, fmt.Errorf("%w: reconcile http %d", ErrContract, st)
	}
	var r struct {
		Found bool            `json:"found"`
		Job   json.RawMessage `json:"job"`
	}
	if err := strictJSON(data, &r); err != nil {
		return false, JobView{}, fmt.Errorf("%w: reconcile %v", ErrContract, err)
	}
	if !r.Found {
		return false, JobView{}, nil
	}
	j, err := decodeJob(200, r.Job)
	return err == nil, j, err
}

// Artifact downloads the bytes of a registered artifact and verifies them
// against the registration: sha256 (body and header), exact size, media type,
// kind and audience. Oversized, truncated or mismatched bodies fail closed.
func (c Client) Artifact(ctx context.Context, tenant string, a model.EduArtifact) ([]byte, string, error) {
	if !serviceIDRe.MatchString(a.ServiceArtifactID) || !strings.HasPrefix(a.ServiceArtifactID, "ma_") {
		return nil, "", ErrContract
	}
	st, data, h, err := c.doLimit(ctx, http.MethodGet, "/v1/artifacts/"+a.ServiceArtifactID, tenant, "platform", nil, MaxArtifactBytes)
	if err != nil {
		return nil, "", err
	}
	if st != http.StatusOK {
		return nil, "", fmt.Errorf("%w: artifact http %d", ErrContract, st)
	}
	sum := sha256.Sum256(data)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != a.SHA256 || h.Get("X-Artifact-Sha256") != a.SHA256 {
		return nil, "", fmt.Errorf("%w: artifact hash mismatch", ErrContract)
	}
	if (a.Size > 0 && int64(len(data)) != a.Size) || (a.MediaType != "" && h.Get("Content-Type") != a.MediaType) ||
		(a.Kind != "" && h.Get("X-Artifact-Kind") != "" && h.Get("X-Artifact-Kind") != a.Kind) ||
		(a.Audience != "" && h.Get("X-Artifact-Audience") != "" && h.Get("X-Artifact-Audience") != a.Audience) {
		return nil, "", fmt.Errorf("%w: artifact metadata mismatch", ErrContract)
	}
	return data, h.Get("Content-Type"), nil
}

// GradeResult is the objective judgement returned by the math service. It never
// carries the expected answer.
type GradeResult struct {
	Result string `json:"result"`
	Reason string `json:"reason"`
	Grader string `json:"grader"`
}

var gradeResults = map[string]bool{"correct": true, "correct_approx": true, "incorrect": true, "needs_exact": true, "needs_teacher": true, "invalid": true}

// Grade asks the service to judge one response against an expected answer (synchronous, isolated on the service side).
func (c Client) Grade(ctx context.Context, tenant string, req map[string]any) (GradeResult, error) {
	var g GradeResult
	st, data, _, err := c.do(ctx, http.MethodPost, "/v1/grade", tenant, "platform", req)
	if err != nil {
		return g, err
	}
	if st != http.StatusOK {
		var e struct {
			Error *ServiceError `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != nil {
			return g, e.Error
		}
		return g, fmt.Errorf("%w: grade http %d", ErrContract, st)
	}
	if json.Unmarshal(data, &g) != nil || !gradeResults[g.Result] {
		return g, fmt.Errorf("%w: grade result", ErrContract)
	}
	return g, nil
}

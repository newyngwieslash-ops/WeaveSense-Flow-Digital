package flow

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// §6.2 / §12 status names shared by reservations, model tasks and MCP calls.
const (
	// StatusReconciliation: an external call may have happened but its outcome is unknown;
	// it is queried/reconciled, never blindly re-sent.
	StatusReconciliation = "reconciliation_required"
	// StatusRetryWait: a retryable failure waiting for its next attempt.
	StatusRetryWait = "retry_wait"
)

// §12 usage types (integer smallest units).
const (
	UsageLLMInputTokens  = "llm_input_tokens"
	UsageLLMOutputTokens = "llm_output_tokens"
	UsageOCRPages        = "ocr_pages"
	UsageASRSeconds      = "asr_seconds"
	UsagePaidSourceCalls = "paid_source_calls"
	UsageMCPCalls        = "mcp_calls"
	UsageStoredBytes     = "stored_bytes"
	UsageNotifications   = "notifications"
	// Canvas agent runs (mirrored from the run's settled billing orders; see app.recordCanvasRunUsage).
	UsageCanvasModelCalls = "canvas_model_calls"
	UsageCanvasMedia      = "canvas_media_generations"
	// Education math workbench: isolated math-service compute time per stage (docs/math-workbench).
	UsageMathComputeMS = "math_compute_ms"
)

var UsageTypes = []string{UsageLLMInputTokens, UsageLLMOutputTokens, UsageOCRPages, UsageASRSeconds, UsagePaidSourceCalls, UsageMCPCalls, UsageStoredBytes, UsageNotifications,
	UsageCanvasModelCalls, UsageCanvasMedia, UsageMathComputeMS}

func validUsageType(t string) bool {
	for _, u := range UsageTypes {
		if u == t {
			return true
		}
	}
	return false
}

// PriceBook is the product-configured price list. Every reservation and usage
// record freezes the Version it was priced with.
type PriceBook struct {
	Version   string           `json:"version"`
	UnitMicro map[string]int64 `json:"unit_micro"`
}

// DefaultPriceBook: LLM tokens are charged through the Model Gateway task cost
// (unit 0 here avoids double charging); OCR pages and ASR seconds carry a price.
var DefaultPriceBook = PriceBook{Version: "price_v1", UnitMicro: map[string]int64{
	UsageLLMInputTokens: 0, UsageLLMOutputTokens: 0, UsageOCRPages: 2000, UsageASRSeconds: 100,
	UsagePaidSourceCalls: 0, UsageMCPCalls: 0, UsageStoredBytes: 0, UsageNotifications: 0,
	UsageCanvasModelCalls: 0, UsageCanvasMedia: 0,
	// Placeholder compute price pending product confirmation (docs/math-workbench/06-数学目录-限制-许可.md).
	UsageMathComputeMS: 1,
}}

// CurrentPriceBook reads FLOW_PRICE_BOOK (JSON {"version","unit_micro"}) or returns the default.
func CurrentPriceBook() PriceBook {
	raw := strings.TrimSpace(os.Getenv("FLOW_PRICE_BOOK"))
	if raw == "" {
		return DefaultPriceBook
	}
	var pb PriceBook
	if err := json.Unmarshal([]byte(raw), &pb); err != nil || pb.Version == "" {
		return DefaultPriceBook
	}
	for k, v := range pb.UnitMicro {
		if !validUsageType(k) || v < 0 {
			return DefaultPriceBook
		}
	}
	return pb
}

var ErrUsageInvalid = errors.New("flow: invalid usage record")

// UsageInput is one metered line. CostMicro >= 0 overrides the price-book cost
// (e.g. the Model Gateway's settled cost); -1 = price from the book.
type UsageInput struct {
	TenantID, WorkspaceID, UsageType, SubjectRef, IdemKey, ReservationID string
	// RunID: price with the run's frozen capability snapshot price book.
	RunID     string
	Quantity  int64
	CostMicro int64
	// Charge: debit the cost now (reserve+settle under the idempotency key).
	Charge bool
}

// RecordUsage writes a usage line once per (tenant, idem key) with the frozen price version.
func RecordUsage(db *gorm.DB, in UsageInput, now time.Time) (model.FlowUsageRecord, error) {
	var out model.FlowUsageRecord
	if in.TenantID == "" || in.IdemKey == "" || !validUsageType(in.UsageType) || in.Quantity < 0 {
		return out, ErrUsageInvalid
	}
	if err := db.First(&out, "tenant_id = ? AND idem_key = ?", in.TenantID, in.IdemKey).Error; err == nil {
		return out, nil
	}
	pb := CurrentPriceBook()
	if in.RunID != "" {
		pb = RunPriceBook(db, in.RunID)
	}
	unit := pb.UnitMicro[in.UsageType]
	cost := in.Quantity * unit
	if in.CostMicro >= 0 {
		cost = in.CostMicro
	}
	status := "recorded"
	resID := in.ReservationID
	if in.Charge && cost > 0 {
		b := Billing{DB: db, Now: func() time.Time { return now }}
		r, err := b.Reserve(in.TenantID, "usage:"+in.IdemKey, "usage:"+in.UsageType, in.SubjectRef, cost)
		switch {
		case err == nil:
			if _, err := b.Settle(r.ID, cost); err != nil {
				return out, err
			}
			status, resID = "charged", r.ID
		case errors.Is(err, ErrBudgetInsufficient) || errors.Is(err, ErrBudgetQuota):
			status = "insufficient"
		default:
			// Never fail the caller (e.g. the event inbox) on a billing-side error; keep the line for reconciliation.
			status = "unbilled"
		}
	}
	out = model.FlowUsageRecord{ID: NewID("use_"), TenantID: in.TenantID, WorkspaceID: in.WorkspaceID, UsageType: in.UsageType, Quantity: in.Quantity,
		UnitPriceMicro: unit, CostMicro: cost, PriceVersion: pb.Version, ReservationID: resID, SubjectRef: truncRunes(in.SubjectRef, 200),
		Status: status, IdemKey: truncRunes(in.IdemKey, 200), CreatedAt: now}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&out).Error; err != nil {
		return out, err
	}
	out = model.FlowUsageRecord{}
	return out, db.First(&out, "tenant_id = ? AND idem_key = ?", in.TenantID, in.IdemKey).Error
}

// UsageSummary aggregates a tenant's usage per type since `from`.
type UsageSummary struct {
	UsageType string `json:"usageType"`
	Quantity  int64  `json:"quantity"`
	CostMicro int64  `json:"costMicro"`
	Records   int64  `json:"records"`
}

func SummarizeUsage(db *gorm.DB, tenantID string, from time.Time) ([]UsageSummary, error) {
	var rows []UsageSummary
	err := db.Model(&model.FlowUsageRecord{}).Select("usage_type, SUM(quantity) AS quantity, SUM(cost_micro) AS cost_micro, COUNT(*) AS records").
		Where("tenant_id = ? AND created_at >= ?", tenantID, from).Group("usage_type").Scan(&rows).Error
	sort.Slice(rows, func(i, j int) bool { return rows[i].UsageType < rows[j].UsageType })
	return rows, err
}

// ModelUsage is filled by a gateway that reports token usage (ctx sink).
type ModelUsage struct{ InputTokens, OutputTokens int64 }

type modelUsageCtx struct{}

// WithModelUsage attaches a sink the gateway fills with prompt/completion tokens.
func WithModelUsage(ctx context.Context, u *ModelUsage) context.Context {
	return context.WithValue(ctx, modelUsageCtx{}, u)
}

func modelUsageFrom(ctx context.Context) *ModelUsage {
	u, _ := ctx.Value(modelUsageCtx{}).(*ModelUsage)
	return u
}

type modelCallMetaCtx struct{}

// ModelCallMeta travels with a gateway request (§12): reservation, frozen price version, stable call id.
type ModelCallMeta struct{ ReservationID, PriceVersion, CallID string }

func WithModelCallMeta(ctx context.Context, m ModelCallMeta) context.Context {
	return context.WithValue(ctx, modelCallMetaCtx{}, m)
}

// recordModelUsage writes llm_input/output token lines for a settled model task.
func recordModelUsage(db *gorm.DB, t model.FlowModelTask, u ModelUsage, cost int64, now time.Time) error {
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return nil
	}
	_, err := DefaultUsageRecorder.Record(db, UsageInput{TenantID: t.TenantID, UsageType: UsageLLMInputTokens, Quantity: u.InputTokens, CostMicro: 0,
		SubjectRef: "model_task:" + t.ID, IdemKey: fmt.Sprintf("model:%s:in", t.IdemKey), ReservationID: t.ReservationID}, now)
	if err != nil {
		return err
	}
	// The settled task cost is attributed to the output line so the total matches the ledger.
	_, err = DefaultUsageRecorder.Record(db, UsageInput{TenantID: t.TenantID, UsageType: UsageLLMOutputTokens, Quantity: u.OutputTokens, CostMicro: cost,
		SubjectRef: "model_task:" + t.ID, IdemKey: fmt.Sprintf("model:%s:out", t.IdemKey), ReservationID: t.ReservationID}, now)
	return err
}

func base64Std(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

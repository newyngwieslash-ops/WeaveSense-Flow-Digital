package flow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PRD §8.1 RunContext.capabilitySnapshotId / §9.1 "每 Run 冻结": at run admission
// the platform freezes one content-addressed capability snapshot — skills
// (id, version, hash), the price book, the tool schemas offered to the model,
// the model route and the policy hash — and binds it to the run. During the run:
//   - tool calls must name a tool of the frozen table (CheckRunTool),
//   - usage is priced with the frozen price book (RunPriceBook),
//   - installed skills are pinned (FlowSkillRunPin) so a mid-run install change has no effect,
//   - engine calls carry capability_snapshot_id in the execution context.

// CapabilityInput is what a run adapter knows at admission.
type CapabilityInput struct {
	TenantID, WorkspaceID, RunID, SubjectKind string
	Skills                                    []SkillRef
	Tools                                     []map[string]any
	ModelRoute                                map[string]any
	PolicyHash                                string
}

type capabilityContent struct {
	SubjectKind string           `json:"subject_kind"`
	Skills      []SkillRef       `json:"skills"`
	PriceBook   PriceBook        `json:"price_book"`
	Tools       []map[string]any `json:"tools"`
	ToolNames   []string         `json:"tool_names"`
	ModelRoute  map[string]any   `json:"model_route"`
	PolicyHash  string           `json:"policy_hash"`
}

func hashJSON(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func toolName(t map[string]any) string {
	if fn, ok := t["function"].(map[string]any); ok {
		if n, ok := fn["name"].(string); ok {
			return n
		}
	}
	if n, ok := t["name"].(string); ok {
		return n
	}
	return ""
}

// FreezeCapabilitySnapshot binds a capability snapshot to the run, once: a
// second call for the same run returns the first binding unchanged.
func FreezeCapabilitySnapshot(db *gorm.DB, in CapabilityInput, now time.Time) (model.FlowCapabilitySnapshot, error) {
	var snap model.FlowCapabilitySnapshot
	if in.RunID == "" || in.TenantID == "" {
		return snap, errors.New("flow: capability snapshot needs run and tenant")
	}
	if snap, err := RunCapability(db, in.RunID); err == nil {
		return snap, nil
	}
	skills := append([]SkillRef{}, in.Skills...)
	if in.WorkspaceID != "" && in.SubjectKind != SubjectCanvas {
		installed, err := ListInstalledSkills(db, Scope{TenantID: in.TenantID, WorkspaceID: in.WorkspaceID})
		if err != nil {
			return snap, err
		}
		for _, i := range installed {
			skills = append(skills, SkillRef{ID: i.SkillID, Version: i.Version, Hash: i.Hash})
			if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.FlowSkillRunPin{RunID: in.RunID, SkillID: i.SkillID, TenantID: in.TenantID,
				Version: i.Version, Hash: i.Hash, CreatedAt: now}).Error; err != nil {
				return snap, err
			}
		}
	}
	sort.Slice(skills, func(a, b int) bool { return skills[a].ID+skills[a].Version < skills[b].ID+skills[b].Version })
	names := []string{}
	for _, t := range in.Tools {
		if n := toolName(t); n != "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	c := capabilityContent{SubjectKind: in.SubjectKind, Skills: skills, PriceBook: CurrentPriceBook(), Tools: in.Tools, ToolNames: names, ModelRoute: in.ModelRoute, PolicyHash: in.PolicyHash}
	raw, _ := json.Marshal(c)
	// The id is per tenant: snapshot rows are tenant-scoped under RLS, so two
	// tenants with identical capabilities must not share one row.
	sum := sha256.Sum256(append([]byte(in.TenantID+"\x00"), raw...))
	id := "cap_" + hex.EncodeToString(sum[:])[:32]
	snap = model.FlowCapabilitySnapshot{ID: id, TenantID: in.TenantID, Content: string(raw), PriceVersion: c.PriceBook.Version,
		SkillsHash: hashJSON(skills), ToolSchemaHash: hashJSON(in.Tools), ModelRouteHash: hashJSON(in.ModelRoute), CreatedAt: now}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&snap).Error; err != nil {
		return snap, err
	}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.FlowRunCapability{RunID: in.RunID, TenantID: in.TenantID, SnapshotID: id, CreatedAt: now}).Error; err != nil {
		return snap, err
	}
	return RunCapability(db, in.RunID)
}

// RunCapability returns the snapshot bound to a run.
func RunCapability(db *gorm.DB, runID string) (model.FlowCapabilitySnapshot, error) {
	var snap model.FlowCapabilitySnapshot
	var rc model.FlowRunCapability
	if runID == "" || db == nil {
		return snap, gorm.ErrRecordNotFound
	}
	if err := db.First(&rc, "run_id = ?", runID).Error; err != nil {
		return snap, err
	}
	return snap, db.First(&snap, "id = ?", rc.SnapshotID).Error
}

func decodeCapability(s model.FlowCapabilitySnapshot) (capabilityContent, error) {
	var c capabilityContent
	return c, json.Unmarshal([]byte(s.Content), &c)
}

// ErrToolNotInSnapshot: the call names a tool that was not in the run's frozen tool table.
var ErrToolNotInSnapshot = errors.New("flow: tool is not in this run's capability snapshot")

// ErrNoCapabilitySnapshot: the run carries no frozen snapshot. Checks fail
// closed — a run without a snapshot never bypasses tool/model enforcement.
var ErrNoCapabilitySnapshot = errors.New("flow: run has no capability snapshot")

// ErrModelNotInSnapshot: the call targets a model outside the run's frozen model route.
var ErrModelNotInSnapshot = errors.New("flow: model is not in this run's capability snapshot")

func runContent(db *gorm.DB, runID string) (capabilityContent, error) {
	snap, err := RunCapability(db, runID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return capabilityContent{}, ErrNoCapabilitySnapshot
		}
		return capabilityContent{}, err
	}
	return decodeCapability(snap)
}

// CheckRunTool enforces the frozen tool table (fail-closed: no snapshot or an
// empty frozen table rejects every tool).
func CheckRunTool(db *gorm.DB, runID, name string) error {
	c, err := runContent(db, runID)
	if err != nil {
		return err
	}
	for _, n := range c.ToolNames {
		if n == name {
			return nil
		}
	}
	return ErrToolNotInSnapshot
}

// FrozenModels lists every model identifier of the run's frozen model route.
func FrozenModels(route map[string]any) []string {
	out := []string{}
	add := func(v any) {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	for _, k := range []string{"channel_model_key", "model", "logical_model_id"} {
		add(route[k])
	}
	if mg, ok := route["flow_model_gateway"].(map[string]any); ok {
		add(mg["model"])
	}
	return out
}

// CheckRunModel enforces the frozen model route (§9.2): a model call must
// target a model of the run's snapshot. Fail-closed like CheckRunTool.
func CheckRunModel(db *gorm.DB, runID, modelID string) error {
	c, err := runContent(db, runID)
	if err != nil {
		return err
	}
	for _, m := range FrozenModels(c.ModelRoute) {
		if m == modelID {
			return nil
		}
	}
	return ErrModelNotInSnapshot
}

// RunPriceBook is the run's frozen price book, or the current one.
func RunPriceBook(db *gorm.DB, runID string) PriceBook {
	if snap, err := RunCapability(db, runID); err == nil {
		if c, err := decodeCapability(snap); err == nil && c.PriceBook.Version != "" {
			return c.PriceBook
		}
	}
	return CurrentPriceBook()
}

type capabilityCtxKey struct{}

// WithCapabilitySnapshot carries the snapshot id to engine calls (execution context claim).
func WithCapabilitySnapshot(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, capabilityCtxKey{}, id)
}

func capabilityFromCtx(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(capabilityCtxKey{}).(string)
	return s
}

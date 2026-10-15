package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// PRD §8.1 / §12: canvas agent runs meter into the flow usage ledger through the
// canvas run chain's UsageRecorder. Every billing order of the run's tasks (Pi
// model steps and media generations) becomes one usage line, once per order
// (idempotency key canvas:order:<id>). The credit debit itself is the task's
// billing order on the same CreditAccount the flow ledger settles against, so
// the line is recorded with Charge=false (never a second debit) and carries the
// order's settled amount as its cost.
func (s *Service) recordCanvasRunUsage(run *model.CloudAgentExecution) {
	if s == nil || s.repo == nil || run == nil || run.ID == "" {
		return
	}
	db := s.repo.DB()
	var ids []string
	req := CloudAgentRequest{}
	if state, err := cloudAgentDecode(run); err == nil {
		ids = append(ids, state.TaskIDs...)
		req = state.Request
	}
	var tasks []model.Task
	q := db.Where("user_id = ? AND agent_run_id = ?", run.UserID, run.ID)
	if len(ids) > 0 {
		q = db.Where("user_id = ? AND (agent_run_id = ? OR id IN ?)", run.UserID, run.ID, ids)
	}
	if err := q.Find(&tasks).Error; err != nil {
		return
	}
	chain := s.agentRunChain(run.ID, req)
	tenant := model.PersonalTenantID(run.UserID)
	now := time.Now().UTC()
	for _, t := range tasks {
		if t.BillingOrderID == "" {
			continue
		}
		var order model.BillingOrder
		if err := db.First(&order, "id = ? AND user_id = ?", t.BillingOrderID, run.UserID).Error; err != nil {
			continue
		}
		if order.Status != model.BillingStatusSettled {
			continue // reserved/refunded orders are not usage
		}
		cost := order.ActualAmountMicrocredits
		if cost <= 0 {
			cost = order.AmountMicrocredits
		}
		_, _ = chain.Usage.Record(db, flow.UsageInput{TenantID: tenant, UsageType: canvasUsageType(t), Quantity: 1, CostMicro: cost,
			SubjectRef: run.ID + ":" + t.ID, IdemKey: "canvas:order:" + order.ID, RunID: run.ID, Charge: false}, now)
	}
}

func canvasUsageType(t model.Task) string {
	ty := strings.ToLower(t.Type)
	if strings.Contains(ty, "image") || strings.Contains(ty, "video") || strings.Contains(ty, "audio") || strings.Contains(ty, "media") || t.GenerationID != "" {
		return flow.UsageCanvasMedia
	}
	return flow.UsageCanvasModelCalls
}

// freezeRunCapability binds the run's capability snapshot at admission (both adapters).
// A freeze failure is returned (never ignored): the caller fails the run.
func (s *Service) freezeRunCapability(userID, runID string, req CloudAgentRequest, tools []map[string]any, policy any, skills []flow.SkillRef) error {
	if s == nil || s.repo == nil || runID == "" {
		return errors.New("capability snapshot: no run")
	}
	in := flow.CapabilityInput{TenantID: model.PersonalTenantID(userID), RunID: runID, SubjectKind: flow.SubjectCanvas, Skills: skills, Tools: tools,
		ModelRoute: map[string]any{"channel_id": req.ChannelID, "channel_model_key": req.ChannelModelKey, "model": req.Model, "logical_model_id": req.LogicalModelID},
		PolicyHash: capabilityPolicyHash(policy)}
	if cloudAgentIntelSubject(req) {
		if _, ss, err := s.cloudAgentFlowConversation(userID, req); err == nil {
			in.TenantID, in.WorkspaceID, in.SubjectKind = ss.Scope.TenantID, ss.Scope.WorkspaceID, ss.Subject.Kind
		}
		if mg := flow.ModelGatewayFromEnv(); mg != nil {
			in.ModelRoute["flow_model_gateway"] = map[string]any{"model": mg.Model, "micro_per_1k_tokens": mg.MicroPer1KTok}
		}
	}
	return s.inFlowUser(userID, func(db *gorm.DB) error {
		_, err := flow.FreezeCapabilitySnapshot(db, in, time.Now().UTC())
		return err
	})
}

// checkRunModel enforces the run's frozen model route for one model call.
func (s *Service) checkRunModel(userID, runID, modelID string) error {
	if s == nil || s.repo == nil {
		return errors.New("capability snapshot: no repository")
	}
	return s.inFlowUser(userID, func(db *gorm.DB) error { return flow.CheckRunModel(db, runID, modelID) })
}

func capabilityPolicyHash(policy any) string {
	b, _ := json.Marshal(policy)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func cloudAgentSkillRefs(skills []cloudAgentSkill) []flow.SkillRef {
	out := make([]flow.SkillRef, 0, len(skills))
	for _, sk := range skills {
		out = append(out, flow.SkillRef{ID: sk.ID, Version: sk.Version, Hash: sk.Hash})
	}
	return out
}

package app

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"

	"gorm.io/gorm"
)

// PRD §8.1: every agent run goes through the same five interfaces
// (flow.ScopeResolver, ContextProvider, ToolProvider, ToolExecutor,
// UsageRecorder). Intelligence subjects use flow.IntelRunChain (flowRunChain);
// canvas runs use the canvas adapter below. agentRunChain picks the adapter by subject.

// agentRunChainHook lets tests observe every chain the runtime builds. Nil in production.
var agentRunChainHook atomic.Pointer[func(flow.RunChain) flow.RunChain]

func hookAgentRunChain(c flow.RunChain) flow.RunChain {
	if h := agentRunChainHook.Load(); h != nil {
		return (*h)(c)
	}
	return c
}

// agentRunChain returns the §8.1 run chain for a run request.
func (s *Service) agentRunChain(runID string, req CloudAgentRequest) flow.RunChain {
	if cloudAgentIntelSubject(req) {
		return s.flowRunChain(runID, req)
	}
	return s.canvasRunChain(nil, nil)
}

// canvasRunChain builds the canvas adapter. ctx/params are only needed by the
// ContextProvider (Pi request build); other callers pass nil.
func (s *Service) canvasRunChain(ctx context.Context, params *EnhancedPiRequestParams) flow.RunChain {
	return hookAgentRunChain(flow.RunChain{
		Kind:    flow.SubjectCanvas,
		Scope:   canvasScopeResolver{s: s},
		Context: canvasContextProvider{s: s, ctx: ctx, params: params},
		Tools:   canvasToolProvider{},
		Exec:    canvasToolExecutor{},
		Usage:   flow.DefaultUsageRecorder,
	})
}

// canvasScopeResolver: a canvas is owned by its user; the run may only target
// a canvas the authenticated user owns (re-checked on every run admission).
type canvasScopeResolver struct{ s *Service }

func (r canvasScopeResolver) ResolveSubject(_ *gorm.DB, userID string, subj flow.AgentSubject) (flow.SubjectScope, error) {
	if subj.Kind != flow.SubjectCanvas || strings.TrimSpace(subj.ID) == "" {
		return flow.SubjectScope{}, flow.ErrForbidden
	}
	if _, err := r.s.repo.CanvasProjectForUser(userID, subj.ID); err != nil {
		return flow.SubjectScope{}, err
	}
	return flow.SubjectScope{Subject: subj}, nil
}

// canvasContextProvider assembles the enhanced canvas snapshot for the Pi request.
type canvasContextProvider struct {
	s      *Service
	ctx    context.Context
	params *EnhancedPiRequestParams
}

func (p canvasContextProvider) TurnContext(_ *gorm.DB, _ model.FlowAgentConversation, ss flow.SubjectScope) (flow.TurnContext, error) {
	if p.params == nil {
		return flow.TurnContext{}, errors.New("canvas context provider needs run parameters")
	}
	ctx := p.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	data, err := p.s.buildPiCanvasData(ctx, *p.params)
	if err != nil {
		return flow.TurnContext{}, err
	}
	return flow.TurnContext{Subject: ss.Subject, Canvas: data}, nil
}

// canvasToolRequest is the canvas ToolProvider payload.
type canvasToolRequest struct {
	Req            CloudAgentRequest
	IncludeProfile bool
}

type canvasToolProvider struct{}

func (canvasToolProvider) Tools(req flow.ToolRequest) []map[string]any {
	p, _ := req.Payload.(canvasToolRequest)
	return compileCanvasAgentTools(p.Req, p.IncludeProfile)
}

// canvasToolExecution is the canvas ToolExecutor payload: the run checkpoint
// being mutated plus reads prepared outside the write transaction.
type canvasToolExecution struct {
	Mode             canvasExecMode
	Svc              *Service
	MediaRequest     CreateTaskRequest
	Plan             *cloudAgentMediaPlan
	Repo             *repository.Repository
	Run              *model.CloudAgentExecution
	State            *cloudAgentRuntime
	Call             cloudAgentCall
	Policy           RuntimePolicySetting
	Allowed          bool
	InspectionResult any
	InspectionErr    error
	SkillResult      any
	SkillErr         error
}

// canvasExecMode selects how a canvas call is dispatched.
type canvasExecMode int

const (
	// canvasExecCheckpoint runs a tool inside the run's checkpoint transaction.
	canvasExecCheckpoint canvasExecMode = iota
	// canvasExecRead runs a batched read before the checkpoint write.
	canvasExecRead
	// canvasExecMedia hands an approved generate_media / image_layer_split call to the media runner.
	canvasExecMedia
	// canvasExecEnqueue submits an auto-admitted media task.
	canvasExecEnqueue
)

// dispatchCanvasTool runs a call that has no in-process result (media and task
// submission) through the canvas run chain's ToolExecutor.
func (s *Service) dispatchCanvasTool(p canvasToolExecution) error {
	_, err := s.agentRunChain(p.Run.ID, p.State.Request).Exec.Execute(context.Background(), nil, flow.ToolCall{
		UserID: p.Run.UserID, RunID: p.Run.ID, CallID: p.Call.ID, Name: p.Call.Function.Name, Payload: p})
	return err
}

type canvasToolExecutor struct{}

func (canvasToolExecutor) Execute(_ context.Context, _ *gorm.DB, c flow.ToolCall) (flow.ToolResult, error) {
	p, ok := c.Payload.(canvasToolExecution)
	if !ok || p.Run == nil || p.State == nil {
		return flow.ToolResult{CallID: c.CallID, Tool: c.Name, Status: "failed"}, errors.New("canvas tool executor: missing run state")
	}
	var result any
	var err error
	switch p.Mode {
	case canvasExecRead:
		result, err = cloudAgentReadToolCached(p.Repo, p.Run.UserID, p.State, p.Call, p.Svc)
	case canvasExecMedia:
		err = p.Svc.advanceCloudAgentMedia(p.Run, p.State, cloudAgentMediaCall(p.Call))
	case canvasExecEnqueue:
		err = p.Svc.enqueueCloudAgentTask(p.Run, p.State, p.MediaRequest, p.Plan)
	default:
		if p.Repo == nil {
			return flow.ToolResult{CallID: c.CallID, Tool: c.Name, Status: "failed"}, errors.New("canvas tool executor: missing repository")
		}
		result, err = executeCanvasTool(p)
	}
	status := "succeeded"
	if err != nil {
		status = "failed"
	}
	return flow.ToolResult{CallID: c.CallID, Tool: c.Name, Status: status, Value: result}, err
}

func executeCanvasTool(p canvasToolExecution) (result any, toolErr error) {
	call := p.Call
	switch {
	case !p.Allowed:
		// 幻觉出来的工具名与"真被权限挡住"要分开反馈：前者模型根本不该发这个调用，
		// 后者是授权边界问题。两者仍然一律拒绝，只是给模型的下一步更明确。
		if !cloudAgentPlatformToolNames()[call.Function.Name] {
			toolErr = BadAuthRequest("模型调用了不存在的工具「" + truncateRunes(call.Function.Name, 60) + "」，本轮已拒绝；请只使用本轮工具表里列出的工具")
		} else {
			toolErr = BadAuthRequest("工具未获本轮权限授权")
		}
	case call.Function.Name == "canvas_apply_ops":
		result, toolErr = applyCloudAgentCanvas(p.Repo, p.Run.UserID, p.State.Request.CanvasID, call, p.Policy, cloudAgentCanvasEventRecorder(p.Run.ID, p.State))
	case call.Function.Name == "canvas_arrange_nodes":
		result, toolErr = applyCloudAgentArrangeNodes(p.Repo, p.Run.UserID, p.State.Request.CanvasID, call, p.Policy, cloudAgentCanvasEventRecorder(p.Run.ID, p.State))
	case call.Function.Name == "canvas_create_storyboard", call.Function.Name == "canvas_edit_storyboard":
		result, toolErr = applyCloudAgentStoryboardMutation(p.Repo, p.Run.UserID, p.State.Request.CanvasID, call, p.Policy, cloudAgentCanvasEventRecorder(p.Run.ID, p.State))
	case call.Function.Name == "canvas_create_character":
		result, toolErr = applyCloudAgentCharacterCreate(p.Repo, p.Run.UserID, p.State.Request.CanvasID, call, p.Policy, cloudAgentCanvasEventRecorder(p.Run.ID, p.State))
	case call.Function.Name == "canvas_edit_batch_table":
		result, toolErr = applyCloudAgentBatchTableMutation(p.Repo, p.Run.UserID, p.State.Request.CanvasID, call, p.Policy, cloudAgentCanvasEventRecorder(p.Run.ID, p.State))
	case call.Function.Name == "previs_scene_create", call.Function.Name == "previs_apply_patch":
		result, toolErr = applyCloudAgentPrevisMutation(p.Repo, p.Run.UserID, p.State.Request.CanvasID, call, p.Policy, cloudAgentCanvasEventRecorder(p.Run.ID, p.State))
	case call.Function.Name == "canvas_inspect_image":
		result, toolErr = p.InspectionResult, p.InspectionErr
		if toolErr == nil && p.InspectionResult != nil {
			if inspection, ok := p.InspectionResult.(cloudAgentImageInspection); ok {
				p.State.markCanvasImageInspection(stringValue(inspection.Receipt["nodeId"]), strings.TrimSpace(inspection.ImageURL) != "")
				if inspection.CacheKey != "" {
					if p.State.ImageInspectionReads == nil {
						p.State.ImageInspectionReads = map[string]int{}
					}
					p.State.ImageInspectionReads[inspection.CacheKey]++
				}
				if inspection.Receipt["repeat"] == true {
					cloudAgentMarkReadLoopNudge(p.State, call, p.State.ImageInspectionReads[inspection.CacheKey])
				}
			}
		}
	case call.Function.Name == "skill_read_file", call.Function.Name == "model_list", call.Function.Name == "image_annotation_render", call.Function.Name == "canvas_read_text":
		result, toolErr = p.SkillResult, p.SkillErr
	default:
		result, toolErr = cloudAgentReadToolCached(p.Repo, p.Run.UserID, p.State, call)
	}
	return result, toolErr
}

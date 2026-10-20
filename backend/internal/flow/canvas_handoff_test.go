package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"yingce/backend/internal/model"
)

func TestHandoffToCanvasCreateAndAppendWithProvenance(t *testing.T) {
	db := testDB(t)
	sc, err := ResolveScope(db, "u_alice", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

	res, err := HandoffToCanvas(db, sc, "u_alice", HandoffToCanvasInput{
		Kind:        "report",
		SourceID:    "rpt_001",
		Title:       "AI 短剧出海市场深度报告",
		CanvasTitle: "短剧出海分镜企划",
		Content:     "北美与东南亚短剧市场付费率在 Q3 显著上升。",
		Citations: []HandoffCitation{
			{DocumentID: "doc_101", Title: "SensorTower Q3 报告", Snippet: "北美短剧 IAP 环比增长 42%", URL: "https://example.com/q3"},
		},
		AgentPrompt: "基于该报告拆解 4 个黄金前三秒钩子分镜",
	}, now)
	if err != nil {
		t.Fatalf("HandoffToCanvas create failed: %v", err)
	}
	if !res.CreatedCanvas || res.CanvasID == "" || res.NodeID == "" || res.Revision != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !strings.Contains(res.CanvasURL, "/canvas/"+res.CanvasID) || !strings.Contains(res.CanvasURL, "agent=1") || !strings.Contains(res.CanvasURL, "intelNodeId="+res.NodeID) {
		t.Fatalf("unexpected canvas_url: %s", res.CanvasURL)
	}

	var proj model.CanvasProject
	if err := db.First(&proj, "id = ? AND user_id = ?", res.CanvasID, "u_alice").Error; err != nil {
		t.Fatalf("canvas project not stored: %v", err)
	}
	if proj.Title != "短剧出海分镜企划" {
		t.Fatalf("want canvas title 短剧出海分镜企划, got %s", proj.Title)
	}

	var payload struct {
		Nodes []struct {
			ID       string         `json:"id"`
			Type     string         `json:"type"`
			Title    string         `json:"title"`
			Position map[string]int `json:"position"`
			Metadata struct {
				Content     string         `json:"content"`
				IntelSource map[string]any `json:"intelSource"`
			} `json:"metadata"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(proj.PayloadJSON), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(payload.Nodes) != 1 {
		t.Fatalf("want 1 node, got %d", len(payload.Nodes))
	}
	n0 := payload.Nodes[0]
	if n0.ID != res.NodeID || n0.Type != "text" || n0.Title != "AI 短剧出海市场深度报告" {
		t.Fatalf("unexpected node: %+v", n0)
	}
	if !strings.Contains(n0.Metadata.Content, "## 情报来源与证据引用") || !strings.Contains(n0.Metadata.Content, "SensorTower Q3 报告 (`doc_101`)") {
		t.Fatalf("provenance footer missing in content: %s", n0.Metadata.Content)
	}
	if n0.Metadata.IntelSource["kind"] != "report" || n0.Metadata.IntelSource["sourceId"] != "rpt_001" {
		t.Fatalf("unexpected intelSource metadata: %+v", n0.Metadata.IntelSource)
	}

	// Verify mirrored conversation in FlowAgentConversation & FlowCanvasConversationDoc
	var dirConv model.FlowAgentConversation
	if err := db.First(&dirConv, "id = ? AND user_id = ?", res.ConversationID, "u_alice").Error; err != nil {
		t.Fatalf("mirrored directory conversation missing: %v", err)
	}
	if dirConv.SubjectKind != SubjectCanvas || dirConv.SubjectID != res.CanvasID {
		t.Fatalf("unexpected directory conversation subject: %+v", dirConv)
	}

	// Append a second intelligence node to the same canvas
	res2, err := HandoffToCanvas(db, sc, "u_alice", HandoffToCanvasInput{
		CanvasID: res.CanvasID,
		Kind:     "document",
		SourceID: "doc_202",
		Title:    "TikTok 热门短剧评论抽样",
		Content:  "观众对反转剧情的完播率高出均值 65%。",
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("HandoffToCanvas append failed: %v", err)
	}
	if res2.CreatedCanvas || res2.CanvasID != res.CanvasID || res2.Revision != 2 {
		t.Fatalf("unexpected append result: %+v", res2)
	}
	if err := db.First(&proj, "id = ?", res.CanvasID).Error; err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(proj.PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Nodes) != 2 {
		t.Fatalf("want 2 nodes after append, got %d", len(payload.Nodes))
	}
	if payload.Nodes[1].Position["x"] <= payload.Nodes[0].Position["x"] {
		t.Fatalf("expected second node to be placed to the right of first node: n0=%v n1=%v", payload.Nodes[0].Position, payload.Nodes[1].Position)
	}

	// Cross-user isolation: u_bob cannot append to u_alice's canvas
	bobScope, err := ResolveScope(db, "u_bob", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = HandoffToCanvas(db, bobScope, "u_bob", HandoffToCanvasInput{
		CanvasID: res.CanvasID,
		Kind:     "report",
		Title:    "越权写入测试",
		Content:  "不应成功",
	}, now)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound when u_bob writes to u_alice canvas, got %v", err)
	}
}

func TestIntelHandoffToCanvasToolInWorkspaceAndTopic(t *testing.T) {
	db := testDB(t)
	sc, err := ResolveScope(db, "u_alice", "")
	if err != nil {
		t.Fatal(err)
	}
	topTitle := "东南亚短剧受众分析"
	topQuestion := "印尼与泰国用户付费偏好有何差异？"
	topic, err := CreateTopic(db, sc, "u_alice", TopicInput{
		Title:    &topTitle,
		Question: &topQuestion,
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}

	convs := Conversations{DB: db}
	ssTopic, err := ResolveSubject(db, "u_alice", AgentSubject{Kind: SubjectTopic, ID: topic.ID})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := convs.Create("u_alice", ssTopic, "专题投送对话")
	if err != nil {
		t.Fatal(err)
	}

	tools := Tools{DB: db}
	args := map[string]any{
		"title":        "印尼与泰国短剧受众对比",
		"content":      "印尼用户偏好家庭伦理与逆袭题材，泰国用户偏好奇幻浪漫题材。",
		"kind":         "topic",
		"agent_prompt": "为印尼与泰国市场各生成一组角色设定与分镜脚本",
		"citations": []any{
			map[string]any{"document_id": "doc_id_1", "title": "东南亚调研纪要", "snippet": "印尼家庭逆袭完播率第一"},
		},
	}
	tr, err := tools.Invoke(context.Background(), conv, ssTopic, "u_alice", "call_handoff_1", "intel_handoff_to_canvas", args, "")
	if err != nil {
		t.Fatalf("Invoke intel_handoff_to_canvas in topic failed: %v", err)
	}
	if tr.Status != "succeeded" {
		t.Fatalf("expected succeeded, got %s (%s)", tr.Status, tr.Error)
	}
	var out HandoffToCanvasResult
	if err := json.Unmarshal(tr.Output, &out); err != nil {
		t.Fatalf("unmarshal tool output: %v", err)
	}
	if !out.CreatedCanvas || out.CanvasID == "" || out.NodeID == "" {
		t.Fatalf("unexpected tool output: %+v", out)
	}

	// Replay with identical call_id returns cached result without creating a second canvas
	replay, err := tools.Invoke(context.Background(), conv, ssTopic, "u_alice", "call_handoff_1", "intel_handoff_to_canvas", args, "")
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}
	if !replay.Replayed {
		t.Fatalf("expected replay=true")
	}
	var replayOut HandoffToCanvasResult
	_ = json.Unmarshal(replay.Output, &replayOut)
	if replayOut.CanvasID != out.CanvasID || replayOut.NodeID != out.NodeID {
		t.Fatalf("replay created different canvas/node: %+v vs %+v", replayOut, out)
	}
}

func TestCanvasHandoffRESTRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	r := gin.New()
	RegisterRoutes(r.Group("/api"), db, Config{}, func(c *gin.Context) (*model.User, error) {
		uid := c.GetHeader("X-Test-User")
		if uid == "" {
			uid = "u_alice"
		}
		return &model.User{ID: uid, Username: uid}, nil
	})

	body := map[string]any{
		"kind":        "brief",
		"sourceId":    "brief_01",
		"title":       "今日短剧热榜简报",
		"canvasTitle": "热榜改编画布",
		"content":     "TOP1 剧集单日充值破百万，核心看点为强情绪节奏。",
		"agentPrompt": "拆解前三集分镜并生成主角视觉设定图",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/intelligence/canvas-handoff", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201 Created, got %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Code int                   `json:"code"`
		Data HandoffToCanvasResult `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Code != 0 || !env.Data.CreatedCanvas || env.Data.CanvasID == "" {
		t.Fatalf("unexpected response: %+v", env)
	}
}

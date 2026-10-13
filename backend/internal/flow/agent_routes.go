package flow

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"yingce/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// RegisterAgentRoutes mounts the server-side conversation directory and the
// intel tool bridge endpoints. Tenant/workspace/role are re-resolved per request.
func RegisterAgentRoutes(api gin.IRouter, db *gorm.DB, engine EngineCaller, user UserFunc) {
	convs := Conversations{DB: db}
	tools := Tools{DB: db, Engine: engine}
	authed := func(c *gin.Context) (*model.User, bool) {
		u, err := user(c)
		if err != nil || u == nil {
			reply(c, http.StatusUnauthorized, nil, "unauthorized")
			return nil, false
		}
		return u, true
	}
	loadConv := func(c *gin.Context, u *model.User) (model.FlowAgentConversation, SubjectScope, bool) {
		conv, ss, err := convs.Get(u.ID, c.Param("id"))
		if err != nil {
			reply(c, http.StatusNotFound, nil, "conversation not found")
			return conv, ss, false
		}
		return conv, ss, true
	}
	g := api.Group("/agent")
	g.GET("/tools", func(c *gin.Context) {
		reply(c, http.StatusOK, gin.H{"catalog": Catalog, "functions": PiFunctionTools()}, "ok")
	})
	g.GET("/conversations", func(c *gin.Context) {
		u, ok := authed(c)
		if !ok {
			return
		}
		limit, _ := strconv.Atoi(c.Query("limit"))
		var before time.Time
		if v := c.Query("before"); v != "" {
			before, _ = time.Parse(time.RFC3339Nano, v)
		}
		list, err := convs.List(u.ID, limit, before)
		if err != nil {
			reply(c, http.StatusInternalServerError, nil, "list failed")
			return
		}
		reply(c, http.StatusOK, list, "ok")
	})
	g.POST("/conversations", func(c *gin.Context) {
		u, ok := authed(c)
		if !ok {
			return
		}
		var in struct {
			Subject AgentSubject `json:"subject"`
			Title   string       `json:"title"`
		}
		if c.ShouldBindJSON(&in) != nil {
			reply(c, http.StatusBadRequest, nil, "bad request")
			return
		}
		ss, err := ResolveSubject(db, u.ID, in.Subject)
		if err != nil {
			reply(c, http.StatusNotFound, nil, "subject not found")
			return
		}
		conv, err := convs.Create(u.ID, ss, in.Title)
		if err != nil {
			reply(c, http.StatusInternalServerError, nil, "create failed")
			return
		}
		reply(c, http.StatusCreated, conv, "ok")
	})
	g.GET("/conversations/:id/messages", func(c *gin.Context) {
		u, ok := authed(c)
		if !ok {
			return
		}
		conv, _, ok := loadConv(c, u)
		if !ok {
			return
		}
		before, _ := strconv.ParseInt(c.Query("before_seq"), 10, 64)
		limit, _ := strconv.Atoi(c.Query("limit"))
		msgs, next, err := convs.Messages(conv.ID, before, limit)
		if err != nil {
			reply(c, http.StatusInternalServerError, nil, "messages failed")
			return
		}
		runs, _ := convs.Runs(conv)
		reply(c, http.StatusOK, gin.H{"messages": msgs, "nextBeforeSeq": next, "runs": runs}, "ok")
	})
	g.POST("/conversations/:id/messages", func(c *gin.Context) {
		u, ok := authed(c)
		if !ok {
			return
		}
		conv, _, ok := loadConv(c, u)
		if !ok {
			return
		}
		var in struct {
			Content string `json:"content"`
			RunID   string `json:"runId"`
		}
		if c.ShouldBindJSON(&in) != nil || in.Content == "" {
			reply(c, http.StatusBadRequest, nil, "content required")
			return
		}
		m, err := convs.Append(conv, model.FlowAgentMessage{Role: "user", Content: in.Content, RunID: in.RunID})
		if err != nil {
			reply(c, http.StatusInternalServerError, nil, "append failed")
			return
		}
		reply(c, http.StatusCreated, m, "ok")
	})
	g.GET("/conversations/:id/context", func(c *gin.Context) {
		u, ok := authed(c)
		if !ok {
			return
		}
		conv, ss, ok := loadConv(c, u)
		if !ok {
			return
		}
		tc, err := BuildTurnContext(db, conv, ss)
		if err != nil {
			reply(c, http.StatusInternalServerError, nil, "context failed")
			return
		}
		reply(c, http.StatusOK, gin.H{"context": tc, "messages": tc.Messages(), "tools": PiFunctionTools()}, "ok")
	})
	g.POST("/conversations/:id/tool-calls", func(c *gin.Context) {
		u, ok := authed(c)
		if !ok {
			return
		}
		conv, ss, ok := loadConv(c, u)
		if !ok {
			return
		}
		var in struct {
			CallID    string         `json:"callId"`
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		body, _ := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
		if json.Unmarshal(body, &in) != nil || in.CallID == "" || in.Name == "" {
			reply(c, http.StatusBadRequest, nil, "callId and name required")
			return
		}
		r, err := tools.Invoke(c.Request.Context(), conv, ss, u.ID, in.CallID, in.Name, in.Arguments)
		if err != nil {
			reply(c, toolErrStatus(err), nil, err.Error())
			return
		}
		b, _ := json.Marshal(r)
		_, _ = convs.Append(conv, model.FlowAgentMessage{Role: "tool", Content: string(b), ToolCallID: in.CallID, ToolName: in.Name})
		reply(c, http.StatusOK, r, "ok")
	})
	g.POST("/approvals/:id/decision", func(c *gin.Context) {
		u, ok := authed(c)
		if !ok {
			return
		}
		var in struct {
			Approve bool `json:"approve"`
		}
		if c.ShouldBindJSON(&in) != nil {
			reply(c, http.StatusBadRequest, nil, "bad request")
			return
		}
		r, err := tools.Decide(c.Request.Context(), u.ID, c.Param("id"), in.Approve)
		if err != nil {
			reply(c, toolErrStatus(err), nil, err.Error())
			return
		}
		reply(c, http.StatusOK, r, "ok")
	})
}

func toolErrStatus(err error) int {
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrForbidden):
		return http.StatusNotFound
	case errors.Is(err, ErrToolPermission):
		return http.StatusForbidden
	case errors.Is(err, ErrToolArgs), errors.Is(err, ErrToolUnknown):
		return http.StatusBadRequest
	case errors.Is(err, ErrToolConflict):
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

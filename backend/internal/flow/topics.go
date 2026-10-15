package flow

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"yingce/backend/internal/model"
)

// §5.2 saved research topics ("可保存专题"): a named question with keywords and
// a source scope that a topic conversation (SubjectTopic) analyses.

var ErrTopicInput = errors.New("flow: invalid topic")

type TopicInput struct {
	Title     *string   `json:"title"`
	Question  *string   `json:"question"`
	Keywords  *[]string `json:"keywords"`
	SourceIDs *[]string `json:"sourceIds"`
	Notes     *string   `json:"notes"`
}

type TopicView struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	Title       string    `json:"title"`
	Question    string    `json:"question"`
	Keywords    []string  `json:"keywords"`
	SourceIDs   []string  `json:"sourceIds"`
	Notes       string    `json:"notes"`
	CreatedBy   string    `json:"createdBy"`
	Version     int       `json:"version"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func topicView(t model.FlowTopic) TopicView {
	v := TopicView{ID: t.ID, WorkspaceID: t.WorkspaceID, Title: t.Title, Question: t.Question, Notes: t.Notes,
		CreatedBy: t.CreatedBy, Version: t.Version, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, Keywords: []string{}, SourceIDs: []string{}}
	_ = json.Unmarshal([]byte(t.Keywords), &v.Keywords)
	_ = json.Unmarshal([]byte(t.SourceIDs), &v.SourceIDs)
	return v
}

func cleanList(in []string, max int) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		if len(s) > 200 {
			return nil, ErrTopicInput
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) > max {
		return nil, ErrTopicInput
	}
	return out, nil
}

func applyTopicInput(t *model.FlowTopic, in TopicInput) error {
	if in.Title != nil {
		t.Title = strings.TrimSpace(*in.Title)
	}
	if in.Question != nil {
		t.Question = strings.TrimSpace(*in.Question)
	}
	if in.Notes != nil {
		t.Notes = *in.Notes
	}
	if in.Keywords != nil {
		k, err := cleanList(*in.Keywords, 50)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(k)
		t.Keywords = string(b)
	}
	if in.SourceIDs != nil {
		k, err := cleanList(*in.SourceIDs, 100)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(k)
		t.SourceIDs = string(b)
	}
	if t.Title == "" || len(t.Title) > 200 || len(t.Question) > 4000 || len(t.Notes) > 20000 {
		return ErrTopicInput
	}
	return nil
}

func canWriteTopics(s Scope) bool { return s.Role == "admin" || s.Role == "editor" }

func CreateTopic(db *gorm.DB, s Scope, userID string, in TopicInput, now time.Time) (TopicView, error) {
	if !canWriteTopics(s) {
		return TopicView{}, ErrForbidden
	}
	t := model.FlowTopic{ID: NewID("tp"), TenantID: s.TenantID, WorkspaceID: s.WorkspaceID, CreatedBy: userID,
		Version: 1, Keywords: "[]", SourceIDs: "[]", CreatedAt: now, UpdatedAt: now}
	if err := applyTopicInput(&t, in); err != nil {
		return TopicView{}, err
	}
	if err := db.Create(&t).Error; err != nil {
		return TopicView{}, err
	}
	return topicView(t), nil
}

// UpdateTopic applies a partial update; ifVersion > 0 is an optimistic lock.
func UpdateTopic(db *gorm.DB, s Scope, id string, ifVersion int, in TopicInput, now time.Time) (TopicView, error) {
	if !canWriteTopics(s) {
		return TopicView{}, ErrForbidden
	}
	var t model.FlowTopic
	if err := db.First(&t, "id = ? AND tenant_id = ? AND workspace_id = ?", id, s.TenantID, s.WorkspaceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return TopicView{}, ErrForbidden
		}
		return TopicView{}, err
	}
	if ifVersion > 0 && ifVersion != t.Version {
		return TopicView{}, ErrTopicConflict
	}
	if err := applyTopicInput(&t, in); err != nil {
		return TopicView{}, err
	}
	prev := t.Version
	t.Version++
	t.UpdatedAt = now
	r := db.Model(&model.FlowTopic{}).Where("id = ? AND tenant_id = ? AND version = ?", t.ID, s.TenantID, prev).
		Updates(map[string]any{"title": t.Title, "question": t.Question, "keywords": t.Keywords, "source_ids": t.SourceIDs,
			"notes": t.Notes, "version": t.Version, "updated_at": now})
	if r.Error != nil {
		return TopicView{}, r.Error
	}
	if r.RowsAffected == 0 {
		return TopicView{}, ErrTopicConflict
	}
	return topicView(t), nil
}

var ErrTopicConflict = errors.New("flow: topic version conflict")

func ListTopics(db *gorm.DB, s Scope) ([]TopicView, error) {
	var rows []model.FlowTopic
	if err := db.Where("tenant_id = ? AND workspace_id = ?", s.TenantID, s.WorkspaceID).Order("updated_at DESC").Limit(200).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]TopicView, 0, len(rows))
	for _, r := range rows {
		out = append(out, topicView(r))
	}
	return out, nil
}

func GetTopic(db *gorm.DB, s Scope, id string) (TopicView, error) {
	var t model.FlowTopic
	if err := db.First(&t, "id = ? AND tenant_id = ? AND workspace_id = ?", id, s.TenantID, s.WorkspaceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return TopicView{}, ErrForbidden
		}
		return TopicView{}, err
	}
	return topicView(t), nil
}

func topicErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		reply(c, http.StatusNotFound, nil, "topic not found")
	case errors.Is(err, ErrTopicInput):
		reply(c, http.StatusBadRequest, nil, "invalid topic")
	case errors.Is(err, ErrTopicConflict):
		reply(c, http.StatusConflict, nil, "topic version conflict")
	default:
		reply(c, http.StatusInternalServerError, nil, "topic error")
	}
}

// RegisterTopicRoutes: GET/POST /intelligence/topics, GET/PATCH /intelligence/topics/:id.
func RegisterTopicRoutes(api gin.IRouter, db *gorm.DB, user UserFunc) {
	scoped := func(c *gin.Context) (Scope, string, bool) {
		u, err := user(c)
		if err != nil || u == nil {
			reply(c, http.StatusUnauthorized, nil, "unauthorized")
			return Scope{}, "", false
		}
		s, err := ResolveScope(reqDB(c, db), u.ID, c.GetHeader("X-Workspace-Id"))
		if err != nil {
			reply(c, http.StatusNotFound, nil, "workspace not found")
			return Scope{}, "", false
		}
		return s, u.ID, true
	}
	g := api.Group("/intelligence")
	g.GET("/topics", func(c *gin.Context) {
		s, _, ok := scoped(c)
		if !ok {
			return
		}
		out, err := ListTopics(reqDB(c, db), s)
		if err != nil {
			topicErr(c, err)
			return
		}
		reply(c, http.StatusOK, out, "ok")
	})
	g.GET("/topics/:id", func(c *gin.Context) {
		s, _, ok := scoped(c)
		if !ok {
			return
		}
		out, err := GetTopic(reqDB(c, db), s, c.Param("id"))
		if err != nil {
			topicErr(c, err)
			return
		}
		reply(c, http.StatusOK, out, "ok")
	})
	g.POST("/topics", func(c *gin.Context) {
		s, uid, ok := scoped(c)
		if !ok {
			return
		}
		if !canWriteTopics(s) {
			reply(c, http.StatusForbidden, nil, "forbidden")
			return
		}
		var in TopicInput
		if c.ShouldBindJSON(&in) != nil {
			reply(c, http.StatusBadRequest, nil, "bad request")
			return
		}
		out, err := CreateTopic(reqDB(c, db), s, uid, in, time.Now().UTC())
		if err != nil {
			topicErr(c, err)
			return
		}
		reply(c, http.StatusCreated, out, "ok")
	})
	g.PATCH("/topics/:id", func(c *gin.Context) {
		s, _, ok := scoped(c)
		if !ok {
			return
		}
		if !canWriteTopics(s) {
			reply(c, http.StatusForbidden, nil, "forbidden")
			return
		}
		var in struct {
			TopicInput
			IfVersion int `json:"ifVersion"`
		}
		if c.ShouldBindJSON(&in) != nil {
			reply(c, http.StatusBadRequest, nil, "bad request")
			return
		}
		out, err := UpdateTopic(reqDB(c, db), s, c.Param("id"), in.IfVersion, in.TopicInput, time.Now().UTC())
		if err != nil {
			topicErr(c, err)
			return
		}
		reply(c, http.StatusOK, out, "ok")
	})
}

func strSlice(v any) *[]string {
	arr, ok := v.([]any)
	if !ok {
		if s, ok := v.([]string); ok {
			return &s
		}
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return &out
}

func init() {
	Catalog = append(Catalog, ToolSpec{
		Name:        "intel_save_topic",
		Description: "Save (create, or update when topic_id is given) a research topic in the current workspace: title, question, keywords and source scope. Returns the saved topic and its version.",
		Class:       "write", Scope: "intel.reports.write", Timeout: 5 * time.Second, OutputCap: 4000,
		Parameters: obj([]string{"title"}, map[string]any{"topic_id": tStr, "title": tStr, "question": tStr, "keywords": tStrs, "source_ids": tStrs, "notes": tStr}),
		local: func(t Tools, _ context.Context, _ model.FlowAgentConversation, ss SubjectScope, userID string, a map[string]any) (any, error) {
			in := TopicInput{Keywords: strSlice(a["keywords"]), SourceIDs: strSlice(a["source_ids"])}
			for k, dst := range map[string]**string{"title": &in.Title, "question": &in.Question, "notes": &in.Notes} {
				if v, ok := a[k].(string); ok {
					v := v
					*dst = &v
				}
			}
			now := time.Now().UTC()
			if id := argStr(a, "topic_id"); id != "" {
				return UpdateTopic(t.DB, ss.Scope, id, 0, in, now)
			}
			return CreateTopic(t.DB, ss.Scope, userID, in, now)
		},
	})
	topicTools["intel_save_topic"] = true
}

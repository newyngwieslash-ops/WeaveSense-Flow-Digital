package flow

import (
	"errors"
	"strings"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
)

// AgentSubject decouples the agent from a canvasId: a run is about a canvas,
// an intelligence workspace, or a topic inside a workspace.
type AgentSubject struct {
	Kind string `json:"kind"` // canvas | intelligence_workspace | topic
	ID   string `json:"id"`
}

const (
	SubjectCanvas    = "canvas"
	SubjectWorkspace = "intelligence_workspace"
	SubjectTopic     = "topic"
)

// SubjectScope is what the server derives from auth + subject. Clients never
// supply tenant/workspace/role directly.
type SubjectScope struct {
	Subject AgentSubject `json:"subject"`
	Scope   Scope        `json:"scope"`
	TopicID string       `json:"topicId,omitempty"`
}

// ResolveSubject re-resolves tenant/workspace/role for userID on every call.
func ResolveSubject(db *gorm.DB, userID string, s AgentSubject) (SubjectScope, error) {
	s.ID = strings.TrimSpace(s.ID)
	switch s.Kind {
	case SubjectWorkspace:
		sc, err := ResolveScope(db, userID, s.ID)
		return SubjectScope{Subject: s, Scope: sc}, err
	case SubjectTopic:
		var t model.FlowTopic
		if err := db.First(&t, "id = ?", s.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return SubjectScope{}, ErrForbidden
			}
			return SubjectScope{}, err
		}
		sc, err := ResolveScope(db, userID, t.WorkspaceID)
		if err != nil || sc.TenantID != t.TenantID {
			return SubjectScope{}, ErrForbidden
		}
		return SubjectScope{Subject: s, Scope: sc, TopicID: t.ID}, nil
	case SubjectCanvas:
		// Canvas stays owned by the user; intel tools operate on the personal workspace.
		var n int64
		if db.Migrator().HasTable(&model.Canvas{}) {
			if err := db.Model(&model.Canvas{}).Where("id = ? AND user_id = ?", s.ID, userID).Count(&n).Error; err != nil {
				return SubjectScope{}, err
			}
		}
		if n == 0 {
			db.Model(&model.CanvasProject{}).Where("id = ? AND user_id = ?", s.ID, userID).Count(&n)
		}
		if n == 0 {
			// The user's own canvas sidebar document (keyed by user + canvas id).
			db.Model(&model.FlowCanvasConversationDoc{}).Where("canvas_id = ? AND user_id = ?", s.ID, userID).Count(&n)
		}
		if n == 0 {
			return SubjectScope{}, ErrForbidden
		}
		sc, err := ResolveScope(db, userID, "")
		return SubjectScope{Subject: s, Scope: sc}, err
	}
	return SubjectScope{}, errors.New("flow: unknown subject kind")
}

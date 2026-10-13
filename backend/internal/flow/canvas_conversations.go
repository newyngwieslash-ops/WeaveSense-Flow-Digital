package flow

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"yingce/backend/internal/model"
)

const maxCanvasConversationDoc = 4 << 20

// RegisterCanvasConversationRoutes stores the canvas agent sidebar conversation
// directory server-side. Writes use optimistic concurrency on version (409 on
// a stale write) so two devices cannot silently overwrite each other.
func RegisterCanvasConversationRoutes(api gin.IRouter, db *gorm.DB, user UserFunc) {
	g := api.Group("/agent/canvas-conversations")
	auth := func(c *gin.Context) (string, string, bool) {
		u, err := user(c)
		if err != nil || u == nil {
			reply(c, http.StatusUnauthorized, nil, "unauthorized")
			return "", "", false
		}
		id := strings.TrimSpace(c.Param("canvasId"))
		if id == "" || len(id) > 80 {
			reply(c, http.StatusBadRequest, nil, "bad canvas id")
			return "", "", false
		}
		return u.ID, id, true
	}
	g.GET("/:canvasId", func(c *gin.Context) {
		uid, cid, ok := auth(c)
		if !ok {
			return
		}
		var row model.FlowCanvasConversationDoc
		err := db.First(&row, "user_id = ? AND canvas_id = ?", uid, cid).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			reply(c, http.StatusOK, gin.H{"version": 0, "document": nil}, "ok")
			return
		}
		if err != nil {
			reply(c, http.StatusInternalServerError, nil, "load error")
			return
		}
		reply(c, http.StatusOK, gin.H{"version": row.Version, "document": json.RawMessage(row.Document), "updatedAt": row.UpdatedAt}, "ok")
	})
	g.PUT("/:canvasId", func(c *gin.Context) {
		uid, cid, ok := auth(c)
		if !ok {
			return
		}
		var body struct {
			BaseVersion int64           `json:"baseVersion"`
			Document    json.RawMessage `json:"document"`
		}
		if err := json.NewDecoder(io.LimitReader(c.Request.Body, maxCanvasConversationDoc+1024)).Decode(&body); err != nil || len(body.Document) == 0 || !json.Valid(body.Document) {
			reply(c, http.StatusBadRequest, nil, "bad document")
			return
		}
		if len(body.Document) > maxCanvasConversationDoc {
			reply(c, http.StatusRequestEntityTooLarge, nil, "document too large")
			return
		}
		now := time.Now()
		var version int64
		err := db.Transaction(func(tx *gorm.DB) error {
			if body.BaseVersion == 0 {
				r := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.FlowCanvasConversationDoc{UserID: uid, CanvasID: cid, Document: string(body.Document), Version: 1, UpdatedAt: now})
				if r.Error != nil {
					return r.Error
				}
				if r.RowsAffected == 0 {
					return errVersionConflict
				}
				version = 1
				return nil
			}
			r := tx.Model(&model.FlowCanvasConversationDoc{}).Where("user_id = ? AND canvas_id = ? AND version = ?", uid, cid, body.BaseVersion).
				Updates(map[string]any{"document": string(body.Document), "version": body.BaseVersion + 1, "updated_at": now})
			if r.Error != nil {
				return r.Error
			}
			if r.RowsAffected == 0 {
				return errVersionConflict
			}
			version = body.BaseVersion + 1
			return nil
		})
		if errors.Is(err, errVersionConflict) {
			var row model.FlowCanvasConversationDoc
			db.First(&row, "user_id = ? AND canvas_id = ?", uid, cid)
			c.JSON(http.StatusConflict, gin.H{"code": http.StatusConflict, "data": gin.H{"version": row.Version, "document": json.RawMessage(nonEmpty(row.Document))}, "msg": "version conflict"})
			return
		}
		if err != nil {
			reply(c, http.StatusInternalServerError, nil, "save error")
			return
		}
		reply(c, http.StatusOK, gin.H{"version": version}, "ok")
	})
}

var errVersionConflict = errors.New("version conflict")

func nonEmpty(s string) string {
	if s == "" {
		return "null"
	}
	return s
}

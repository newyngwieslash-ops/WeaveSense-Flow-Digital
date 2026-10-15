package flow

import (
	"encoding/json"
	"errors"
	"net/http"

	"yingce/backend/internal/model"

	"github.com/gin-gonic/gin"
)

type proxyFunc func(method, enginePath string) gin.HandlerFunc
type proxyWithFunc func(method, enginePath string, mutate func(body []byte, u *model.User, idem string) ([]byte, error)) gin.HandlerFunc

// directApproval marks a REST monitor save as approved by the signed-in user's own request
// (the agent path gets its approval_id from the approval card instead).
func directApproval(body []byte, u *model.User, idem string) ([]byte, error) {
	if idem == "" {
		return nil, errors.New("X-Idempotency-Key required")
	}
	m := map[string]any{}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &m); err != nil {
			return nil, errors.New("invalid json")
		}
	}
	m["approval_id"] = "user-direct:" + u.ID + ":" + idem
	return json.Marshal(m)
}

// registerContractRoutes adds the PRD §11.1 routes that were only reachable through agent tools.
func registerContractRoutes(g *gin.RouterGroup, proxy proxyFunc, proxyWith proxyWithFunc) {
	g.GET("/sources/health", proxy(http.MethodGet, "/v1/sources/health"))
	g.PATCH("/sources/:id", proxy(http.MethodPatch, "/v1/sources/:id"))
	g.POST("/monitors/preview", proxy(http.MethodPost, "/v1/monitors/preview"))
	g.POST("/monitors", proxyWith(http.MethodPost, "/v1/monitors", directApproval))
	g.GET("/documents/:id/revisions", proxy(http.MethodGet, "/v1/documents/:id/revisions"))
	g.GET("/documents/:id/revisions/:rev", proxy(http.MethodGet, "/v1/documents/:id/revisions/:rev"))
	g.POST("/research-runs", proxy(http.MethodPost, "/v1/research-runs?async=1"))
	g.GET("/research-runs/:id", proxy(http.MethodGet, "/v1/research-runs/:id"))
	g.POST("/research-runs/:id/cancel", proxy(http.MethodPost, "/v1/research-runs/:id/cancel"))
	g.POST("/operations/:id/cancel", proxy(http.MethodPost, "/v1/operations/:id/cancel"))
	g.POST("/documents/ocr", proxy(http.MethodPost, "/v1/documents/ocr"))
	g.POST("/documents/asr", proxy(http.MethodPost, "/v1/documents/asr"))
	g.POST("/evidence/compare", proxy(http.MethodPost, "/v1/evidence/compare"))
	g.GET("/terms", proxy(http.MethodGet, "/v1/terms"))
	g.POST("/terms", proxy(http.MethodPost, "/v1/terms"))
	g.POST("/terms/delete", proxy(http.MethodPost, "/v1/terms/delete"))
}

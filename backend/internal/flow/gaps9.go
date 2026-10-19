package flow

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"yingce/backend/internal/model"
)

// Audit-8 platform side: §5.1 monitor plan pricing + notification targets + re-approval,
// §5.2/§10.3 scoped report export, §10.4 verified media download, §6.4 derived jobs.

// MonitorPriceHint turns the current price book into the per-source-run unit the Engine
// uses for the preview cost estimate. Kinds billed as paid source calls come from
// FLOW_PAID_SOURCE_KINDS (default "wechat": the gateway provider is metered).
func MonitorPriceHint() map[string]any {
	pb := CurrentPriceBook()
	unit := pb.UnitMicro[UsagePaidSourceCalls]
	kinds := strings.TrimSpace(os.Getenv("FLOW_PAID_SOURCE_KINDS"))
	if kinds == "" {
		kinds = "wechat"
	}
	per := map[string]int64{"rss": 0, "telegram": 0, "wechat": 0}
	for _, k := range strings.Split(kinds, ",") {
		if k = strings.TrimSpace(k); k != "" {
			per[k] = unit
		}
	}
	return map[string]any{"version": pb.Version, "per_source_run_micro": per}
}

var ErrUnknownTarget = errors.New("flow: unknown notification target")

// validateTargets: every id must be an active notification target of the caller's workspace.
func validateTargets(db *gorm.DB, s Scope, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var n int64
	if err := db.Model(&model.FlowNotificationTarget{}).Where("tenant_id = ? AND workspace_id = ? AND status = 'active' AND id IN ?", s.TenantID, s.WorkspaceID, ids).Count(&n).Error; err != nil {
		return err
	}
	uniq := map[string]bool{}
	for _, id := range ids {
		uniq[id] = true
	}
	if int(n) != len(uniq) {
		return ErrUnknownTarget
	}
	return nil
}

func anyStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := []string{}
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// prepareMonitorPlan injects pricing and validates notification targets (REST and agent tool).
func prepareMonitorPlan(db *gorm.DB, s Scope, m map[string]any) (map[string]any, error) {
	if err := validateTargets(db, s, anyStrings(m["notification_target_ids"])); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	out["price"] = MonitorPriceHint()
	return out, nil
}

func init() {
	for i := range Catalog {
		if Catalog[i].Name != "intel_preview_monitor" {
			continue
		}
		props := Catalog[i].Parameters["properties"].(map[string]any)
		props["budget_micro_per_window"] = tInt
		props["notification_target_ids"] = tStrs
		props["monitor_id"] = tStr
		Catalog[i].Description = "Preview a monitor plan (keywords, sources, interval, timezone, budget per window, notification targets; monitor_id to change an existing one). Returns a structured plan: extraction method, cost estimate (per window/day/30d, price version), runs per day and which changes widen the approved scope. Does not write configuration; apply it with intel_apply_monitor (approval)."
		Catalog[i].prepare = func(t Tools, _ context.Context, _ model.FlowAgentConversation, ss SubjectScope, _ string, a map[string]any) (map[string]any, error) {
			out, err := prepareMonitorPlan(t.DB, ss.Scope, a)
			if errors.Is(err, ErrUnknownTarget) {
				return nil, fmt.Errorf("%w: notification_target_ids must be active targets of this workspace", ErrToolArgs)
			}
			return out, err
		}
	}
}

// registerGaps9Routes mounts the audit-8 REST routes on /api/intelligence.
func registerGaps9Routes(g *gin.RouterGroup, db *gorm.DB, client EngineClient, cfg Config, scoped func(c *gin.Context) (Scope, *model.User, bool)) {
	call := func(c *gin.Context, method, path, idem string, body []byte) (Scope, int, []byte, bool) {
		if !cfg.Enabled() {
			reply(c, http.StatusServiceUnavailable, nil, "loom engine not configured")
			return Scope{}, 0, nil, false
		}
		s, u, ok := scoped(c)
		if !ok {
			return Scope{}, 0, nil, false
		}
		st, out, err := client.Do(c.Request.Context(), s, Actor{Type: "user", ID: u.ID}, method, path, idem, body)
		if err != nil {
			reply(c, http.StatusBadGateway, nil, "loom engine unavailable")
			return Scope{}, 0, nil, false
		}
		return s, st, out, true
	}
	readBody := func(c *gin.Context) (map[string]any, bool) {
		raw, _ := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
		m := map[string]any{}
		if len(raw) > 0 && json.Unmarshal(raw, &m) != nil {
			reply(c, http.StatusBadRequest, nil, "invalid json")
			return nil, false
		}
		return m, true
	}
	// §5.1 preview: priced plan with validated notification targets.
	g.POST("/monitors/preview", func(c *gin.Context) {
		s, u, ok := scoped(c)
		if !ok {
			return
		}
		m, ok := readBody(c)
		if !ok {
			return
		}
		m, err := prepareMonitorPlan(reqDB(c, db), s, m)
		if err != nil {
			reply(c, http.StatusBadRequest, nil, "notification_target_ids must be active targets of this workspace")
			return
		}
		body, _ := json.Marshal(m)
		st, out, err := client.Do(c.Request.Context(), s, Actor{Type: "user", ID: u.ID}, http.MethodPost, "/v1/monitors/preview", "", body)
		if err != nil {
			reply(c, http.StatusBadGateway, nil, "loom engine unavailable")
			return
		}
		c.Data(st, "application/json", out)
	})
	g.GET("/monitors/previews/:id", func(c *gin.Context) {
		if _, st, out, ok := call(c, http.MethodGet, "/v1/monitors/previews/"+url.PathEscape(c.Param("id")), "", nil); ok {
			c.Data(st, "application/json", out)
		}
	})
	g.GET("/monitors/:id/revisions", func(c *gin.Context) {
		if _, st, out, ok := call(c, http.MethodGet, "/v1/monitors/"+url.PathEscape(c.Param("id"))+"/revisions", "", nil); ok {
			c.Data(st, "application/json", out)
		}
	})
	// §5.1 update: widening (sources, budget, frequency, notification targets) is refused with
	// 409 reapproval_required until the user confirms it explicitly (confirmExpansion=true),
	// which records a user-direct approval id for this change.
	g.PATCH("/monitors/:id", func(c *gin.Context) {
		s, u, ok := scoped(c)
		if !ok {
			return
		}
		if s.Role != "admin" && s.Role != "editor" {
			reply(c, http.StatusForbidden, nil, "forbidden")
			return
		}
		m, ok := readBody(c)
		if !ok {
			return
		}
		if err := validateTargets(reqDB(c, db), s, anyStrings(m["notification_target_ids"])); err != nil {
			reply(c, http.StatusBadRequest, nil, "notification_target_ids must be active targets of this workspace")
			return
		}
		delete(m, "approval_id")
		if confirm, _ := m["confirmExpansion"].(bool); confirm {
			idem := c.GetHeader("X-Idempotency-Key")
			if idem == "" {
				reply(c, http.StatusBadRequest, nil, "X-Idempotency-Key required to confirm an expansion")
				return
			}
			m["approval_id"] = "user-direct:" + u.ID + ":" + idem
		}
		delete(m, "confirmExpansion")
		body, _ := json.Marshal(m)
		st, out, err := client.Do(c.Request.Context(), s, Actor{Type: "user", ID: u.ID}, http.MethodPatch, "/v1/monitors/"+url.PathEscape(c.Param("id")), "", body)
		if err != nil {
			reply(c, http.StatusBadGateway, nil, "loom engine unavailable")
			return
		}
		c.Data(st, "application/json", out)
	})
	// §5.2 / §10.3 report export, scoped by the engine token (foreign ids are 404).
	g.GET("/reports/:id/export", func(c *gin.Context) {
		format := c.DefaultQuery("format", "md")
		_, st, out, ok := call(c, http.MethodGet, "/v1/reports/"+url.PathEscape(c.Param("id"))+"/export?format="+url.QueryEscape(format), "", nil)
		if !ok {
			return
		}
		var env struct {
			Data struct {
				Filename      string `json:"filename"`
				ContentType   string `json:"content_type"`
				Content       string `json:"content"`
				ContentBase64 string `json:"content_base64"`
			} `json:"data"`
		}
		if st != http.StatusOK || json.Unmarshal(out, &env) != nil {
			c.Data(st, "application/json", out)
			return
		}
		payload := []byte(env.Data.Content)
		if env.Data.ContentBase64 != "" {
			b, err := base64.StdEncoding.DecodeString(env.Data.ContentBase64)
			if err != nil {
				reply(c, http.StatusBadGateway, nil, "invalid export payload")
				return
			}
			payload = b
		}
		c.Header("Content-Disposition", contentDisposition(env.Data.Filename))
		c.Header("X-Content-Type-Options", "nosniff")
		c.Data(http.StatusOK, env.Data.ContentType, payload)
	})
	// §10.4 media download: the engine verifies tenant/workspace, that the document
	// references the key and that it was stored, then streams/proxies the private object
	// or issues a short-lived scoped presigned URL (mode=presign).
	g.GET("/media", func(c *gin.Context) {
		q := url.Values{"document_id": {c.Query("document_id")}, "object_key": {c.Query("object_key")}}
		if mode := c.Query("mode"); mode != "" {
			q.Set("mode", mode)
		}
		if exp := c.Query("expires"); exp != "" {
			q.Set("expires", exp)
		}
		if c.Query("mode") == "presign" {
			_, st, out, ok := call(c, http.MethodGet, "/v1/media?"+q.Encode(), "", nil)
			if !ok {
				return
			}
			if st == http.StatusOK {
				c.Header("Cache-Control", "private, no-store")
			}
			c.Data(st, "application/json", out)
			return
		}
		s, u, ok := scoped(c)
		if !ok {
			return
		}
		resp, err := client.DoStream(c.Request.Context(), s, Actor{Type: "user", ID: u.ID}, http.MethodGet, "/v1/media?"+q.Encode(), map[string]string{
			"X-Flow-Accept-Stream": "1",
		})
		if err != nil {
			reply(c, http.StatusBadGateway, nil, "loom engine unavailable")
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK && resp.Header.Get("X-Flow-Media-Stream") == "1" {
			fname, err := url.PathUnescape(resp.Header.Get("X-Flow-Filename"))
			if err != nil || fname == "" {
				fname = "media"
			}
			ct := resp.Header.Get("Content-Type")
			if ct == "" {
				ct = "application/octet-stream"
			}
			c.Header("Content-Disposition", contentDisposition(fname))
			c.Header("X-Content-Type-Options", "nosniff")
			c.Header("Cache-Control", "private, no-store")
			if sha := resp.Header.Get("X-Flow-Sha256"); sha != "" {
				c.Header("X-Content-SHA256", sha)
			}
			if cl := resp.Header.Get("Content-Length"); cl != "" {
				c.Header("Content-Length", cl)
			}
			c.Header("Content-Type", ct)
			c.Status(http.StatusOK)
			_, _ = io.Copy(c.Writer, io.LimitReader(resp.Body, 512<<20))
			return
		}
		out, _ := io.ReadAll(io.LimitReader(resp.Body, 36<<20))
		var env struct {
			Data struct {
				ContentType string `json:"content_type"`
				Filename    string `json:"filename"`
				SHA256      string `json:"sha256"`
				Data        string `json:"data_base64"`
			} `json:"data"`
		}
		if resp.StatusCode != http.StatusOK || json.Unmarshal(out, &env) != nil {
			c.Data(resp.StatusCode, "application/json", out)
			return
		}
		b, err := base64.StdEncoding.DecodeString(env.Data.Data)
		if err != nil {
			reply(c, http.StatusBadGateway, nil, "invalid media payload")
			return
		}
		c.Header("Content-Disposition", contentDisposition(env.Data.Filename))
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Cache-Control", "private, no-store")
		c.Header("X-Content-SHA256", env.Data.SHA256)
		c.Data(http.StatusOK, env.Data.ContentType, b)
	})
	g.GET("/derived-jobs", func(c *gin.Context) {
		q := url.Values{}
		if d := c.Query("document_id"); d != "" {
			q.Set("document_id", d)
		}
		if _, st, out, ok := call(c, http.MethodGet, "/v1/derived-jobs?"+q.Encode(), "", nil); ok {
			c.Data(st, "application/json", out)
		}
	})
	g.GET("/documents/:id/analysis", func(c *gin.Context) {
		if _, st, out, ok := call(c, http.MethodGet, "/v1/documents/"+url.PathEscape(c.Param("id"))+"/analysis", "", nil); ok {
			c.Data(st, "application/json", out)
		}
	})
}

func contentDisposition(name string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' || r < 0x20 {
			return '_'
		}
		return r
	}, name)
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, asciiOnly(safe), url.PathEscape(safe))
}

func asciiOnly(s string) string {
	b := strings.Builder{}
	for _, r := range s {
		if r < 0x80 {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

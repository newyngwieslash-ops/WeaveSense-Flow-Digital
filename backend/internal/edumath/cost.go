package edumath

import (
	"fmt"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// Full delivery cost accounting (DEV-065). The report never invents numbers:
// every line names its source, and lines whose provider is not connected yet
// (TTS, egress) are reported as not metered instead of zero cost.

var (
	periodRe        = regexp.MustCompile(`^20[0-9]{2}-(0[1-9]|1[0-2])$`)
	supportCategory = map[string]bool{"onboarding": true, "review_support": true, "incident": true, "content": true, "billing": true, "other": true}
)

// CostLine is one cost component of the period.
type CostLine struct {
	Component string `json:"component"`
	Source    string `json:"source"`
	Metered   bool   `json:"metered"`
	CostMicro int64  `json:"costMicro"`
	Quantity  int64  `json:"quantity"`
	Unit      string `json:"unit"`
}

// CostReport is the period report with its denominators.
type CostReport struct {
	Period              string           `json:"period"`
	Timezone            string           `json:"timezone"`
	Lines               []CostLine       `json:"lines"`
	TotalMicro          int64            `json:"totalMicro"`
	AcceptedLessons     int64            `json:"acceptedLessons"`
	ActiveClasses       int64            `json:"activeClasses"`
	PerAcceptedLesson   *int64           `json:"perAcceptedLessonMicro"`
	PerActiveClass      *int64           `json:"perActiveClassMicro"`
	JobsByStatus        map[string]int64 `json:"jobsByStatus"`
	SupportByCategory   map[string]int64 `json:"supportByCategoryMicro"`
	SupportUnattributed int64            `json:"supportUnattributed"`
}

func periodRange(p string) (time.Time, time.Time, error) {
	if !periodRe.MatchString(p) {
		return time.Time{}, time.Time{}, fmt.Errorf("period must be YYYY-MM")
	}
	start, err := time.ParseInLocation("2006-01", p, budgetZone())
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return start.UTC(), start.AddDate(0, 1, 0).UTC(), nil
}

// CostReport sums the period for one tenant (jobs by creation time).
func (s *Service) CostReport(tx *gorm.DB, tenantID, period string) (CostReport, error) {
	from, to, err := periodRange(period)
	if err != nil {
		return CostReport{}, err
	}
	r := CostReport{Period: period, Timezone: budgetZone().String(), JobsByStatus: map[string]int64{}, SupportByCategory: map[string]int64{}}
	jobs := "SELECT id FROM edu_math_jobs WHERE tenant_id = ? AND created_at >= ? AND created_at < ?"
	stageSum := func(where string, args ...any) (cost, qty int64, err error) {
		row := struct{ C, Q int64 }{}
		q := "SELECT COALESCE(SUM(r.cost_micro), 0) AS c, COALESCE(SUM(r.compute_ms), 0) AS q FROM edu_stage_runs r JOIN edu_math_jobs j ON j.id = r.job_id " +
			"WHERE j.tenant_id = ? AND j.created_at >= ? AND j.created_at < ? AND " + where
		err = tx.Raw(q, append([]any{tenantID, from, to}, args...)...).Scan(&row).Error
		return row.C, row.Q, err
	}
	add := func(component, source, unit string, cost, qty int64, metered bool) {
		r.Lines = append(r.Lines, CostLine{Component: component, Source: source, Metered: metered, CostMicro: cost, Quantity: qty, Unit: unit})
		r.TotalMicro += cost
	}
	// Compute that produced delivered results, excluding the render stage.
	c, q, err := stageSum("j.status = 'succeeded' AND r.stage <> 'render_preview'")
	if err != nil {
		return r, err
	}
	add("compute_succeeded", "edu_stage_runs", "compute_ms", c, q, true)
	c, q, err = stageSum("j.status = 'succeeded' AND r.stage = 'render_preview'")
	if err != nil {
		return r, err
	}
	add("render", "edu_stage_runs(render_preview)", "compute_ms", c, q, true)
	// Failed, cancelled and retried work is a delivery cost too.
	c, q, err = stageSum("j.status IN ('failed', 'cancelled')")
	if err != nil {
		return r, err
	}
	add("failed_and_cancelled", "edu_stage_runs", "compute_ms", c, q, true)
	// Unknown outcomes still hold budget; reported, not yet a cost.
	var held int64
	tx.Raw("SELECT COALESCE(SUM(budget_micro), 0) FROM edu_math_jobs WHERE tenant_id = ? AND created_at >= ? AND created_at < ? AND settlement = 'open' AND reservation_id <> ''",
		tenantID, from, to).Scan(&held)
	r.Lines = append(r.Lines, CostLine{Component: "open_holds", Source: "edu_math_jobs(settlement=open)", Metered: true, Quantity: held, Unit: "micro_held"})
	var bytes int64
	tx.Raw("SELECT COALESCE(SUM(size), 0) FROM edu_artifacts WHERE tenant_id = ? AND job_id IN ("+jobs+")", tenantID, tenantID, from, to).Scan(&bytes)
	add("storage", "edu_artifacts.size (no storage price configured)", "bytes", 0, bytes, false)
	add("tts", "provider not connected", "characters", 0, 0, false)
	add("egress", "not metered", "bytes", 0, 0, false)
	// Human support with attribution.
	var sup []model.EduSupportCost
	if err := tx.Where("tenant_id = ? AND period = ?", tenantID, period).Find(&sup).Error; err != nil {
		return r, err
	}
	var supCost, supMin int64
	for _, x := range sup {
		supCost += x.CostMicro
		supMin += int64(x.Minutes)
		r.SupportByCategory[x.Category] += x.CostMicro
		if x.SubjectRef == "" {
			r.SupportUnattributed++
		}
	}
	add("human_support", "edu_support_costs", "minutes", supCost, supMin, true)
	// Status counts and denominators.
	var st []struct {
		Status string
		N      int64
	}
	tx.Raw("SELECT status, COUNT(*) AS n FROM edu_math_jobs WHERE tenant_id = ? AND created_at >= ? AND created_at < ? GROUP BY status", tenantID, from, to).Scan(&st)
	for _, x := range st {
		r.JobsByStatus[x.Status] = x.N
	}
	tx.Model(&model.EduPublication{}).Where("tenant_id = ? AND created_at >= ? AND created_at < ?", tenantID, from, to).Count(&r.AcceptedLessons)
	tx.Raw(`SELECT COUNT(DISTINCT a.class_id) FROM edu_attempts t JOIN edu_assignments a ON a.id = t.assignment_id
		WHERE t.tenant_id = ? AND t.created_at >= ? AND t.created_at < ?`, tenantID, from, to).Scan(&r.ActiveClasses)
	if r.AcceptedLessons > 0 {
		v := r.TotalMicro / r.AcceptedLessons
		r.PerAcceptedLesson = &v
	}
	if r.ActiveClasses > 0 {
		v := r.TotalMicro / r.ActiveClasses
		r.PerActiveClass = &v
	}
	return r, nil
}

func (h *API) registerCost(g *gin.RouterGroup) {
	g.GET("/cost-report", h.costReport)
	g.GET("/support-costs", h.supportCosts)
	g.POST("/support-costs", h.addSupportCost)
}

func (h *API) costReport(c *gin.Context) {
	a, tx, ok := h.ledgerAdmin(c)
	if !ok {
		return
	}
	p := c.Query("period")
	if p == "" {
		p, _ = budgetPeriod(h.Svc.now())
	}
	r, err := h.Svc.CostReport(tx, a.TenantID, p)
	if err != nil {
		if periodRe.MatchString(p) {
			h.fail(c, err)
			return
		}
		reply(c, 400, nil, "period must be YYYY-MM")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	reply(c, 200, r, "ok")
}

func (h *API) supportCosts(c *gin.Context) {
	a, tx, ok := h.ledgerAdmin(c)
	if !ok {
		return
	}
	p := c.Query("period")
	if !periodRe.MatchString(p) {
		reply(c, 400, nil, "period must be YYYY-MM")
		return
	}
	var rows []model.EduSupportCost
	if err := tx.Where("tenant_id = ? AND period = ?", a.TenantID, p).Order("created_at, id").Find(&rows).Error; err != nil {
		h.fail(c, err)
		return
	}
	reply(c, 200, rows, "ok")
}

func (h *API) addSupportCost(c *gin.Context) {
	a, tx, ok := h.ledgerAdmin(c)
	if !ok {
		return
	}
	var in struct {
		Period     string `json:"period"`
		Category   string `json:"category"`
		Minutes    int    `json:"minutes"`
		CostMicro  int64  `json:"costMicro"`
		SubjectRef string `json:"subjectRef"`
		Note       string `json:"note"`
	}
	if c.ShouldBindJSON(&in) != nil || !periodRe.MatchString(in.Period) || !supportCategory[in.Category] || in.Minutes <= 0 || in.Minutes > 100000 ||
		in.CostMicro < 0 || len(in.SubjectRef) > 80 || len([]rune(in.Note)) > 200 {
		reply(c, 400, nil, "period YYYY-MM, known category, minutes 1..100000, costMicro >= 0")
		return
	}
	row := model.EduSupportCost{ID: flow.NewID("esc_"), TenantID: a.TenantID, Period: in.Period, Category: in.Category, Minutes: in.Minutes, CostMicro: in.CostMicro,
		SubjectRef: in.SubjectRef, Note: in.Note, RecordedBy: a.UserID, CreatedAt: h.Svc.now()}
	if err := tx.Create(&row).Error; err != nil {
		h.fail(c, err)
		return
	}
	h.Svc.audit(tx, a, "support_cost.add", row.ID, fmt.Sprintf("%s %s %dmin %d", in.Period, in.Category, in.Minutes, in.CostMicro))
	reply(c, 201, row, "ok")
}

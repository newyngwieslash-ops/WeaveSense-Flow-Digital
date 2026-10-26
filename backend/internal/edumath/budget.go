package edumath

import (
	"fmt"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// Organization budgets and fairness (DEV-064). The platform ledger stays the
// only place money moves; these limits only decide whether a NEW paid job may
// be accepted. Reading anything already produced is never gated by budget.

var activeStatuses = []string{"queued", "running", "reconciliation_required"}

// renderKinds produce rendered lesson media (the expensive, concurrency-bound work).
var renderKinds = []string{"lesson_build", "lesson_edit"}

// budgetZone is the calendar used for monthly periods (EDU_BUDGET_TZ, default Asia/Shanghai).
func budgetZone() *time.Location {
	name := os.Getenv("EDU_BUDGET_TZ")
	if name == "" {
		name = "Asia/Shanghai"
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.UTC
}

func budgetPeriod(now time.Time) (string, time.Time) {
	t := now.In(budgetZone())
	start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	return start.Format("2006-01"), start.UTC()
}

func (s *Service) budget(tx *gorm.DB, tenantID string) model.EduBudget {
	b := model.EduBudget{TenantID: tenantID, AlertPercent: 80}
	tx.Where("tenant_id = ?", tenantID).Limit(1).Find(&b)
	if b.AlertPercent <= 0 || b.AlertPercent > 100 {
		b.AlertPercent = 80
	}
	return b
}

// committed is what the period already costs: settled stage charges plus the
// full hold of jobs whose reservation is still open (unknown outcomes included).
func committed(tx *gorm.DB, tenantID, userID string, since time.Time) (int64, error) {
	var v int64
	q := `SELECT COALESCE(SUM(CASE WHEN j.settlement = 'open' AND j.reservation_id <> '' THEN j.budget_micro
		ELSE (SELECT COALESCE(SUM(r.cost_micro), 0) FROM edu_stage_runs r WHERE r.job_id = j.id) END), 0)
		FROM edu_math_jobs j WHERE j.tenant_id = ? AND j.created_at >= ?`
	args := []any{tenantID, since}
	if userID != "" {
		q += " AND j.created_by = ?"
		args = append(args, userID)
	}
	err := tx.Raw(q, args...).Scan(&v).Error
	return v, err
}

func activeJobs(tx *gorm.DB, tenantID, userID string, kinds []string) (int64, error) {
	var n int64
	q := tx.Model(&model.EduMathJob{}).Where("tenant_id = ? AND status IN ?", tenantID, activeStatuses)
	if userID != "" {
		q = q.Where("created_by = ?", userID)
	}
	if len(kinds) > 0 {
		q = q.Where("kind IN ?", kinds)
	}
	return n, q.Count(&n).Error
}

func (s *Service) raiseAlert(tx *gorm.DB, a Actor, period, scope, level string, spent, limit int64) {
	row := model.EduBudgetAlert{ID: flow.NewID("eba_"), TenantID: a.TenantID, Period: period, Scope: scope, Level: level, SpentMicro: spent, LimitMicro: limit, CreatedAt: s.now()}
	if ins := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row); ins.Error == nil && ins.RowsAffected == 1 {
		s.audit(tx, a, "budget.alert", row.ID, fmt.Sprintf("%s %s %s %d/%d", period, scope, level, spent, limit))
	}
}

// admitJob runs before any reservation: fairness caps first (429, retry later),
// then the organization and teacher monthly budgets (402). On Postgres the
// check is serialized per tenant so concurrent starts cannot overshoot a cap.
func (s *Service) admitJob(tx *gorm.DB, a Actor, kind string, hold int64, paid bool) error {
	if tx.Dialector.Name() == "postgres" {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "edu-budget:"+a.TenantID).Error; err != nil {
			return err
		}
	}
	b := s.budget(tx, a.TenantID)
	type capCheck struct {
		max   int
		user  string
		kinds []string
		what  string
	}
	for _, c := range []capCheck{{b.MaxActiveJobs, "", nil, "organization"}, {b.MaxActivePerTeacher, a.UserID, nil, "teacher"}} {
		if c.max <= 0 {
			continue
		}
		n, err := activeJobs(tx, a.TenantID, c.user, c.kinds)
		if err != nil {
			return err
		}
		if n >= int64(c.max) {
			return appErr(429, "RATE_LIMITED", fmt.Sprintf("%s already has %d running math jobs (limit %d); nothing was dispatched", c.what, n, c.max), "retry_later")
		}
	}
	if b.MaxRenderJobs > 0 && containsKind(renderKinds, kind) {
		n, err := activeJobs(tx, a.TenantID, "", renderKinds)
		if err != nil {
			return err
		}
		if n >= int64(b.MaxRenderJobs) {
			return appErr(429, "RATE_LIMITED", fmt.Sprintf("organization already has %d lesson renders running (limit %d); nothing was dispatched", n, b.MaxRenderJobs), "retry_later")
		}
	}
	if !paid {
		return nil
	}
	period, since := budgetPeriod(s.now())
	for _, lim := range []struct {
		max   int64
		user  string
		scope string
	}{{b.MonthlyMicro, "", "organization"}, {b.TeacherMonthlyMicro, a.UserID, "teacher:" + a.UserID}} {
		if lim.max <= 0 {
			continue
		}
		spent, err := committed(tx, a.TenantID, lim.user, since)
		if err != nil {
			return err
		}
		if spent+hold > lim.max {
			s.raiseAlert(tx, a, period, lim.scope, "exhausted", spent, lim.max)
			return appErr(402, "BUDGET_EXCEEDED", fmt.Sprintf("%s monthly math budget would be exceeded (%d committed + %d hold > %d); nothing was dispatched; existing lessons stay readable", lim.scope, spent, hold, lim.max), "raise_budget")
		}
		if (spent+hold)*100 >= lim.max*int64(b.AlertPercent) {
			s.raiseAlert(tx, a, period, lim.scope, "warn", spent+hold, lim.max)
		}
	}
	return nil
}

func containsKind(xs []string, k string) bool {
	for _, x := range xs {
		if x == k {
			return true
		}
	}
	return false
}

func (h *API) registerBudget(g *gin.RouterGroup) {
	g.GET("/budget", h.getBudget)
	g.PUT("/budget", h.setBudget)
}

// getBudget: staff see the limits, organization and own spend, running counts
// and this period's alerts; teacher-scope alerts of other teachers are admin only.
func (h *API) getBudget(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	b := h.Svc.budget(tx, a.TenantID)
	period, since := budgetPeriod(h.Svc.now())
	org, err := committed(tx, a.TenantID, "", since)
	if err != nil {
		h.fail(c, err)
		return
	}
	mine, _ := committed(tx, a.TenantID, a.UserID, since)
	active, _ := activeJobs(tx, a.TenantID, "", nil)
	myActive, _ := activeJobs(tx, a.TenantID, a.UserID, nil)
	renders, _ := activeJobs(tx, a.TenantID, "", renderKinds)
	q := tx.Where("tenant_id = ? AND period = ?", a.TenantID, period)
	if !canManageRoles(tx, a.TenantID, a.UserID) {
		q = q.Where("scope IN ?", []string{"organization", "teacher:" + a.UserID})
	}
	var alerts []model.EduBudgetAlert
	q.Order("created_at, id").Find(&alerts)
	reply(c, 200, gin.H{"budget": b, "period": period, "timezone": budgetZone().String(), "organizationCommittedMicro": org, "myCommittedMicro": mine,
		"activeJobs": active, "myActiveJobs": myActive, "activeRenderJobs": renders, "alerts": alerts}, "ok")
}

func (h *API) setBudget(c *gin.Context) {
	a, tx, ok := h.ledgerAdmin(c)
	if !ok {
		return
	}
	var in struct {
		MonthlyMicro        int64  `json:"monthlyMicro"`
		TeacherMonthlyMicro int64  `json:"teacherMonthlyMicro"`
		MaxActiveJobs       int    `json:"maxActiveJobs"`
		MaxActivePerTeacher int    `json:"maxActivePerTeacher"`
		MaxRenderJobs       int    `json:"maxRenderJobs"`
		AlertPercent        int    `json:"alertPercent"`
		Reason              string `json:"reason"`
	}
	if c.ShouldBindJSON(&in) != nil || in.MonthlyMicro < 0 || in.TeacherMonthlyMicro < 0 || in.MaxActiveJobs < 0 || in.MaxActivePerTeacher < 0 ||
		in.MaxRenderJobs < 0 || in.AlertPercent < 0 || in.AlertPercent > 100 || in.MaxActiveJobs > 10000 || len(in.Reason) > 200 {
		reply(c, 400, nil, "limits must be non-negative (0 = no limit); alertPercent 1..100")
		return
	}
	if in.AlertPercent == 0 {
		in.AlertPercent = 80
	}
	b := model.EduBudget{TenantID: a.TenantID, MonthlyMicro: in.MonthlyMicro, TeacherMonthlyMicro: in.TeacherMonthlyMicro, MaxActiveJobs: in.MaxActiveJobs,
		MaxActivePerTeacher: in.MaxActivePerTeacher, MaxRenderJobs: in.MaxRenderJobs, AlertPercent: in.AlertPercent, UpdatedBy: a.UserID, UpdatedAt: h.Svc.now()}
	if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&b).Error; err != nil {
		h.fail(c, err)
		return
	}
	h.Svc.audit(tx, a, "budget.set", a.TenantID, fmt.Sprintf("monthly=%d teacher=%d active=%d/%d render=%d alert=%d %s", b.MonthlyMicro, b.TeacherMonthlyMicro,
		b.MaxActiveJobs, b.MaxActivePerTeacher, b.MaxRenderJobs, b.AlertPercent, in.Reason))
	reply(c, 200, gin.H{"budget": b}, "ok")
}

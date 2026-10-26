package edumath

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// Ledger reconciliation and audit export (DEV-063, AC-46, FR-B01). For each math
// job of a tenant it joins the job, its root budget reservation, the per stage
// child reservations, EduStageRun rows and the usage lines, and flags every
// disagreement. Findings persist as EduLedgerAnomaly with an owner and status.

// UnknownStaleAfter: a submit outcome still unknown after this long is an anomaly.
const UnknownStaleAfter = time.Hour

// Anomaly kinds.
const (
	AnomStageUnbilled   = "stage_unbilled"         // stage cost > 0 but no settled child reservation
	AnomStageMismatch   = "stage_child_mismatch"   // child reservation consumed != stage cost
	AnomDuplicateCharge = "duplicate_stage_charge" // more than one child reservation per stage
	AnomUsageMismatch   = "usage_mismatch"         // usage lines disagree with stage runs (count, cost or price version)
	AnomRootMismatch    = "root_mismatch"          // root consumed != children consumed, or root still open after close
	AnomSettlementStuck = "settlement_stuck"       // terminal job whose settlement never closed
	AnomOverBudget      = "over_budget"            // charged more than the frozen job budget
	AnomUnknownStale    = "unknown_outcome_stale"  // reconciliation_required longer than UnknownStaleAfter
	AnomHoldAfterClose  = "hold_after_close"       // released/settled job whose root still holds budget
)

// LedgerLine is one job in the reconciliation report and the audit export.
type LedgerLine struct {
	JobID          string    `json:"jobId"`
	Kind           string    `json:"kind"`
	Status         string    `json:"status"`
	Settlement     string    `json:"settlement"`
	PriceVersion   string    `json:"priceVersion"`
	BudgetMicro    int64     `json:"budgetMicro"`
	ReservationID  string    `json:"reservationId"`
	RootStatus     string    `json:"rootStatus"`
	RootReserved   int64     `json:"rootReservedMicro"`
	RootConsumed   int64     `json:"rootConsumedMicro"`
	RootAllocated  int64     `json:"rootAllocatedMicro"`
	Stages         int       `json:"stages"`
	ComputeMS      int64     `json:"computeMs"`
	StageCost      int64     `json:"stageCostMicro"`
	ChildConsumed  int64     `json:"childConsumedMicro"`
	UsageLines     int       `json:"usageLines"`
	UsageCost      int64     `json:"usageCostMicro"`
	ServiceJobID   string    `json:"serviceJobId"`
	CreatedAt      time.Time `json:"createdAt"`
	Anomalies      []string  `json:"anomalies"`
	AnomalyDetails []string  `json:"-"`
}

// LedgerReport builds the reconciliation lines for jobs created in [from, to).
func (s *Service) LedgerReport(tx *gorm.DB, tenantID string, from, to time.Time) ([]LedgerLine, error) {
	var jobs []model.EduMathJob
	if err := tx.Where("tenant_id = ? AND created_at >= ? AND created_at < ?", tenantID, from, to).Order("created_at, id").Find(&jobs).Error; err != nil {
		return nil, err
	}
	now := s.now()
	out := make([]LedgerLine, 0, len(jobs))
	for _, j := range jobs {
		l := LedgerLine{JobID: j.ID, Kind: j.Kind, Status: j.Status, Settlement: j.Settlement, PriceVersion: j.PriceVersion, BudgetMicro: j.BudgetMicro,
			ReservationID: j.ReservationID, ServiceJobID: j.ServiceJobID, CreatedAt: j.CreatedAt, Anomalies: []string{}}
		flag := func(kind, detail string) {
			l.Anomalies = append(l.Anomalies, kind)
			l.AnomalyDetails = append(l.AnomalyDetails, detail)
		}
		var stages []model.EduStageRun
		tx.Where("job_id = ?", j.ID).Order("stage").Find(&stages)
		l.Stages = len(stages)
		var children []model.FlowBudgetReservation
		if j.ReservationID != "" {
			var root model.FlowBudgetReservation
			if tx.First(&root, "id = ?", j.ReservationID).Error == nil {
				l.RootStatus, l.RootReserved, l.RootConsumed, l.RootAllocated = root.Status, root.ReservedMicro, root.ConsumedMicro, root.AllocatedMicro
			} else {
				l.RootStatus = "missing"
				flag(AnomRootMismatch, "root reservation "+j.ReservationID+" not found")
			}
			tx.Where("parent_id = ?", j.ReservationID).Find(&children)
		}
		perStage := map[string][]model.FlowBudgetReservation{}
		for _, c := range children {
			stage := strings.TrimPrefix(c.RefID, j.ID+":")
			perStage[stage] = append(perStage[stage], c)
			l.ChildConsumed += c.ConsumedMicro
		}
		for _, st := range stages {
			l.ComputeMS += st.ComputeMS
			l.StageCost += st.CostMicro
			cs := perStage[st.Stage]
			if len(cs) > 1 {
				flag(AnomDuplicateCharge, fmt.Sprintf("stage %s has %d child reservations", st.Stage, len(cs)))
			}
			if st.CostMicro > 0 && j.ReservationID != "" {
				if len(cs) == 0 || st.ReservationID == "" {
					flag(AnomStageUnbilled, fmt.Sprintf("stage %s cost %d has no child reservation", st.Stage, st.CostMicro))
				} else {
					var sum int64
					for _, c := range cs {
						sum += c.ConsumedMicro
					}
					if sum != st.CostMicro {
						flag(AnomStageMismatch, fmt.Sprintf("stage %s cost %d, child consumed %d", st.Stage, st.CostMicro, sum))
					}
				}
			}
		}
		var usage []model.FlowUsageRecord
		tx.Where("tenant_id = ? AND subject_ref LIKE ?", tenantID, "edu_job:"+j.ID+":%").Find(&usage)
		l.UsageLines = len(usage)
		badVersion := 0
		for _, u := range usage {
			l.UsageCost += u.CostMicro
			if u.PriceVersion != j.PriceVersion {
				badVersion++
			}
		}
		if l.UsageLines != l.Stages || l.UsageCost != l.StageCost || badVersion > 0 {
			flag(AnomUsageMismatch, fmt.Sprintf("usage lines %d cost %d (%d off price version %s); stages %d cost %d",
				l.UsageLines, l.UsageCost, badVersion, j.PriceVersion, l.Stages, l.StageCost))
		}
		if j.ReservationID != "" && l.RootStatus != "missing" && l.RootConsumed != l.ChildConsumed && l.RootStatus != "reserved" {
			flag(AnomRootMismatch, fmt.Sprintf("root consumed %d, children consumed %d", l.RootConsumed, l.ChildConsumed))
		}
		if j.BudgetMicro > 0 && l.StageCost > j.BudgetMicro {
			flag(AnomOverBudget, fmt.Sprintf("charged %d over budget %d", l.StageCost, j.BudgetMicro))
		}
		terminal := j.Status == "succeeded" || j.Status == "failed" || j.Status == "cancelled"
		if terminal && j.Settlement == "open" {
			flag(AnomSettlementStuck, "terminal job "+j.Status+" with open settlement")
		}
		if (j.Settlement == "settled" || j.Settlement == "released") && j.ReservationID != "" && l.RootStatus == "reserved" {
			flag(AnomHoldAfterClose, "job settlement "+j.Settlement+" but root reservation still holds budget")
		}
		if j.Status == "reconciliation_required" && now.Sub(j.UpdatedAt) > UnknownStaleAfter {
			flag(AnomUnknownStale, "submit outcome unknown since "+j.UpdatedAt.UTC().Format(time.RFC3339))
		}
		out = append(out, l)
	}
	return out, nil
}

// RecordLedgerAnomalies persists the findings: new ones open, still present ones
// refresh LastSeenAt (a resolved one that reappears is reopened), and returns
// the tenant's unresolved anomalies.
func (s *Service) RecordLedgerAnomalies(tx *gorm.DB, a Actor, lines []LedgerLine) ([]model.EduLedgerAnomaly, error) {
	now := s.now()
	for _, l := range lines {
		for i, kind := range l.Anomalies {
			row := model.EduLedgerAnomaly{ID: flow.NewID("ela_"), TenantID: a.TenantID, JobID: l.JobID, Kind: kind, Detail: truncate(l.AnomalyDetails[i], 400),
				Status: "open", DetectedAt: now, LastSeenAt: now}
			ins := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
			if ins.Error != nil {
				return nil, ins.Error
			}
			if ins.RowsAffected == 1 {
				s.audit(tx, a, "ledger.anomaly", row.ID, kind+" "+l.JobID)
				continue
			}
			upd := map[string]any{"last_seen_at": now, "detail": row.Detail}
			var cur model.EduLedgerAnomaly
			if tx.First(&cur, "tenant_id = ? AND job_id = ? AND kind = ?", a.TenantID, l.JobID, kind).Error == nil && cur.Status == "resolved" {
				upd["status"], upd["resolved_at"], upd["resolved_by"] = "open", nil, ""
				s.audit(tx, a, "ledger.anomaly.reopen", cur.ID, kind+" "+l.JobID)
			}
			tx.Model(&model.EduLedgerAnomaly{}).Where("tenant_id = ? AND job_id = ? AND kind = ?", a.TenantID, l.JobID, kind).Updates(upd)
		}
	}
	var open []model.EduLedgerAnomaly
	err := tx.Where("tenant_id = ? AND status <> ?", a.TenantID, "resolved").Order("detected_at, id").Find(&open).Error
	return open, err
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

var errLedgerRange = errors.New("invalid range")

func ledgerRange(c *gin.Context, now time.Time) (time.Time, time.Time, error) {
	from, to := now.AddDate(0, 0, -31), now.Add(time.Minute)
	if v := c.Query("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return from, to, errLedgerRange
		}
		from = t
	}
	if v := c.Query("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return from, to, errLedgerRange
		}
		to = t
	}
	if !to.After(from) || to.Sub(from) > 366*24*time.Hour {
		return from, to, errLedgerRange
	}
	return from.UTC(), to.UTC(), nil
}

func (h *API) registerLedger(g *gin.RouterGroup) {
	g.GET("/ledger/report", h.ledgerReport)
	g.POST("/ledger/reconcile", h.ledgerReconcile)
	g.GET("/ledger/anomalies", h.ledgerAnomalies)
	g.POST("/ledger/anomalies/:id", h.ledgerAnomalyUpdate)
}

// ledgerAdmin: tenant owner/admin or education org_admin; teachers and students never see the ledger.
func (h *API) ledgerAdmin(c *gin.Context) (Actor, *gorm.DB, bool) {
	a, tx, ok := h.staff(c)
	if !ok {
		return a, tx, false
	}
	if !canManageRoles(tx, a.TenantID, a.UserID) {
		h.fail(c, ErrForbidden)
		return a, tx, false
	}
	return a, tx, true
}

// ledgerReport is the audit export: JSON (default) or CSV of every job line in range.
func (h *API) ledgerReport(c *gin.Context) {
	a, tx, ok := h.ledgerAdmin(c)
	if !ok {
		return
	}
	from, to, err := ledgerRange(c, h.Svc.now())
	if err != nil {
		reply(c, 400, nil, "from/to must be RFC3339, to after from, at most 366 days")
		return
	}
	lines, err := h.Svc.LedgerReport(tx, a.TenantID, from, to)
	if err != nil {
		h.fail(c, err)
		return
	}
	h.Svc.audit(tx, a, "ledger.export", a.TenantID, from.Format(time.RFC3339)+"/"+to.Format(time.RFC3339)+" "+c.DefaultQuery("format", "json"))
	if c.Query("format") == "csv" {
		c.Header("Content-Type", "text/csv; charset=utf-8")
		c.Header("Content-Disposition", `attachment; filename="edu-ledger.csv"`)
		c.Header("Cache-Control", "private, no-store")
		c.Status(http.StatusOK)
		w := csv.NewWriter(c.Writer)
		_ = w.Write([]string{"job_id", "kind", "status", "settlement", "price_version", "budget_micro", "reservation_id", "root_status",
			"root_reserved_micro", "root_consumed_micro", "stages", "compute_ms", "stage_cost_micro", "child_consumed_micro",
			"usage_lines", "usage_cost_micro", "service_job_id", "created_at_utc", "anomalies"})
		for _, l := range lines {
			i := strconv.FormatInt
			_ = w.Write([]string{l.JobID, l.Kind, l.Status, l.Settlement, l.PriceVersion, i(l.BudgetMicro, 10), l.ReservationID, l.RootStatus,
				i(l.RootReserved, 10), i(l.RootConsumed, 10), strconv.Itoa(l.Stages), i(l.ComputeMS, 10), i(l.StageCost, 10), i(l.ChildConsumed, 10),
				strconv.Itoa(l.UsageLines), i(l.UsageCost, 10), l.ServiceJobID, l.CreatedAt.UTC().Format(time.RFC3339), strings.Join(l.Anomalies, ";")})
		}
		w.Flush()
		return
	}
	var total LedgerLine
	for _, l := range lines {
		total.StageCost += l.StageCost
		total.ChildConsumed += l.ChildConsumed
		total.UsageCost += l.UsageCost
		total.ComputeMS += l.ComputeMS
	}
	reply(c, 200, gin.H{"from": from, "to": to, "lines": lines, "totals": gin.H{"jobs": len(lines), "computeMs": total.ComputeMS,
		"stageCostMicro": total.StageCost, "childConsumedMicro": total.ChildConsumed, "usageCostMicro": total.UsageCost}}, "ok")
}

func (h *API) ledgerReconcile(c *gin.Context) {
	a, tx, ok := h.ledgerAdmin(c)
	if !ok {
		return
	}
	from, to, err := ledgerRange(c, h.Svc.now())
	if err != nil {
		reply(c, 400, nil, "from/to must be RFC3339, to after from, at most 366 days")
		return
	}
	lines, err := h.Svc.LedgerReport(tx, a.TenantID, from, to)
	if err != nil {
		h.fail(c, err)
		return
	}
	open, err := h.Svc.RecordLedgerAnomalies(tx, a, lines)
	if err != nil {
		h.fail(c, err)
		return
	}
	found := 0
	for _, l := range lines {
		found += len(l.Anomalies)
	}
	h.Svc.audit(tx, a, "ledger.reconcile", a.TenantID, fmt.Sprintf("jobs=%d findings=%d", len(lines), found))
	reply(c, 200, gin.H{"jobs": len(lines), "findings": found, "open": open}, "ok")
}

func (h *API) ledgerAnomalies(c *gin.Context) {
	a, tx, ok := h.ledgerAdmin(c)
	if !ok {
		return
	}
	q := tx.Where("tenant_id = ?", a.TenantID)
	if st := c.Query("status"); st != "" {
		q = q.Where("status = ?", st)
	}
	var rows []model.EduLedgerAnomaly
	q.Order("detected_at, id").Limit(500).Find(&rows)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Status != "resolved" && rows[j].Status == "resolved" })
	reply(c, 200, rows, "ok")
}

// ledgerAnomalyUpdate assigns an owner and moves the handling status; resolving needs a note.
func (h *API) ledgerAnomalyUpdate(c *gin.Context) {
	a, tx, ok := h.ledgerAdmin(c)
	if !ok {
		return
	}
	var in struct {
		Status string `json:"status"`
		Owner  string `json:"owner"`
		Note   string `json:"note"`
	}
	if c.ShouldBindJSON(&in) != nil || (in.Status != "open" && in.Status != "investigating" && in.Status != "resolved") || len(in.Owner) > 40 || len([]rune(in.Note)) > 400 {
		reply(c, 400, nil, "status open|investigating|resolved, owner ≤40, note ≤400")
		return
	}
	if in.Status == "resolved" && strings.TrimSpace(in.Note) == "" {
		reply(c, 400, nil, "resolving an anomaly needs a note")
		return
	}
	if in.Status == "investigating" && in.Owner == "" {
		reply(c, 400, nil, "investigating needs an owner")
		return
	}
	var row model.EduLedgerAnomaly
	if tx.First(&row, "id = ? AND tenant_id = ?", c.Param("id"), a.TenantID).Error != nil {
		h.fail(c, ErrNotFound)
		return
	}
	upd := map[string]any{"status": in.Status, "note": strings.TrimSpace(in.Note)}
	if in.Owner != "" {
		upd["owner"] = in.Owner
	}
	if in.Status == "resolved" {
		now := h.Svc.now()
		upd["resolved_at"], upd["resolved_by"] = now, a.UserID
	} else {
		upd["resolved_at"], upd["resolved_by"] = nil, ""
	}
	tx.Model(&row).Updates(upd)
	h.Svc.audit(tx, a, "ledger.anomaly."+in.Status, row.ID, row.Kind+" "+row.JobID)
	tx.First(&row, "id = ?", row.ID)
	reply(c, 200, row, "ok")
}

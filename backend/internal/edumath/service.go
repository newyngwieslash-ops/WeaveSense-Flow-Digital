package edumath

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// AppError carries a PRD error code (FR-P06) to the HTTP layer.
type AppError struct {
	Status     int
	Code       string
	Message    string
	NextAction string
}

func (e *AppError) Error() string { return e.Code + ": " + e.Message }

func appErr(status int, code, msg, next string) *AppError {
	return &AppError{Status: status, Code: code, Message: msg, NextAction: next}
}

// Service holds the platform-side math workbench operations.
type Service struct {
	DB     *gorm.DB // base handle (system context) for background work
	Client Client
	URLKey []byte
	Now    func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// JobBudgetMicro is the per-job reservation ceiling shown before dispatch (FR-B02).
func JobBudgetMicro() int64 {
	if v, err := strconv.ParseInt(os.Getenv("EDU_MATH_JOB_BUDGET_MICRO"), 10, 64); err == nil && v > 0 {
		return v
	}
	return 120000
}

func unitPrice(pb flow.PriceBook) int64 { return pb.UnitMicro[flow.UsageMathComputeMS] }

func hashOf(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func canonicalJSON(raw json.RawMessage) (string, map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return "", nil, appErr(400, "INPUT_AMBIGUOUS", "structured problem must be a JSON object", "teacher_confirm_input")
	}
	b, _ := json.Marshal(m) // encoding/json sorts map keys → canonical
	if len(b) > 16<<10 {
		return "", nil, appErr(400, "PARAMETER_LIMIT", "structured problem too large", "adjust_parameters")
	}
	return string(b), m, nil
}

func (s *Service) audit(tx *gorm.DB, a Actor, action, subject, detail string) {
	tx.Create(&model.EduAuditEvent{ID: flow.NewID("eau_"), TenantID: a.TenantID, ActorID: a.UserID, Action: action, Subject: subject, Detail: detail, CreatedAt: s.now()})
}

// ---------------------------------------------------------------------------
// Problems

func (s *Service) CreateProblem(tx *gorm.DB, a Actor, familyID, title string, structured json.RawMessage) (model.EduProblem, model.EduProblemRevision, error) {
	if !a.Has("teacher") {
		return model.EduProblem{}, model.EduProblemRevision{}, ErrNoRole
	}
	canon, m, err := canonicalJSON(structured)
	if err != nil {
		return model.EduProblem{}, model.EduProblemRevision{}, err
	}
	if fid, _ := m["family_id"].(string); fid != familyID || familyID == "" {
		return model.EduProblem{}, model.EduProblemRevision{}, appErr(400, "INPUT_AMBIGUOUS", "family_id must match the structured problem", "teacher_confirm_input")
	}
	now := s.now()
	p := model.EduProblem{ID: flow.NewID("epb_"), TenantID: a.TenantID, WorkspaceID: a.WorkspaceID, FamilyID: familyID, Title: title, LatestRevision: 1, CreatedBy: a.UserID, CreatedAt: now, UpdatedAt: now}
	r := model.EduProblemRevision{ID: flow.NewID("epr_"), TenantID: a.TenantID, ProblemID: p.ID, Revision: 1, Structured: canon, InputHash: hashOf(canon),
		State: "needs_confirmation", CreatedBy: a.UserID, CreatedAt: now}
	if err := tx.Create(&p).Error; err != nil {
		return p, r, err
	}
	if err := tx.Create(&r).Error; err != nil {
		return p, r, err
	}
	s.audit(tx, a, "problem.create", p.ID, r.InputHash)
	return p, r, nil
}

// ReviseProblem creates a new revision; confirmed revisions are never edited in place.
func (s *Service) ReviseProblem(tx *gorm.DB, a Actor, problemID string, baseRevision int, structured json.RawMessage) (model.EduProblemRevision, error) {
	var p model.EduProblem
	if err := tx.First(&p, "id = ? AND tenant_id = ? AND workspace_id = ?", problemID, a.TenantID, a.WorkspaceID).Error; err != nil {
		return model.EduProblemRevision{}, ErrNotFound
	}
	if !a.Has("teacher") {
		return model.EduProblemRevision{}, ErrNoRole
	}
	canon, m, err := canonicalJSON(structured)
	if err != nil {
		return model.EduProblemRevision{}, err
	}
	if fid, _ := m["family_id"].(string); fid != p.FamilyID {
		return model.EduProblemRevision{}, appErr(400, "INPUT_AMBIGUOUS", "family_id cannot change within a problem", "teacher_confirm_input")
	}
	res := tx.Model(&model.EduProblem{}).Where("id = ? AND latest_revision = ?", p.ID, baseRevision).
		Updates(map[string]any{"latest_revision": baseRevision + 1, "updated_at": s.now()})
	if res.Error != nil {
		return model.EduProblemRevision{}, res.Error
	}
	if res.RowsAffected == 0 {
		return model.EduProblemRevision{}, appErr(409, "REVISION_CONFLICT", fmt.Sprintf("base revision %d is not the latest", baseRevision), "reload_revision")
	}
	r := model.EduProblemRevision{ID: flow.NewID("epr_"), TenantID: a.TenantID, ProblemID: p.ID, Revision: baseRevision + 1, Structured: canon,
		InputHash: hashOf(canon), State: "needs_confirmation", CreatedBy: a.UserID, CreatedAt: s.now()}
	if err := tx.Create(&r).Error; err != nil {
		return r, err
	}
	s.audit(tx, a, "problem.revise", r.ID, r.InputHash)
	return r, nil
}

func (s *Service) revision(tx *gorm.DB, a Actor, id string) (model.EduProblemRevision, model.EduProblem, error) {
	var r model.EduProblemRevision
	var p model.EduProblem
	if err := tx.First(&r, "id = ? AND tenant_id = ?", id, a.TenantID).Error; err != nil {
		return r, p, ErrNotFound
	}
	if err := tx.First(&p, "id = ? AND tenant_id = ? AND workspace_id = ?", r.ProblemID, a.TenantID, a.WorkspaceID).Error; err != nil {
		return r, p, ErrNotFound
	}
	return r, p, nil
}

// ConfirmProblem binds the teacher's confirmation to the exact input hash (FR-T03).
func (s *Service) ConfirmProblem(tx *gorm.DB, a Actor, revisionID, inputHash string) (model.EduProblemRevision, error) {
	r, _, err := s.revision(tx, a, revisionID)
	if err != nil {
		return r, err
	}
	if !a.Has("teacher") {
		return r, ErrNoRole
	}
	if r.InputHash != inputHash {
		return r, appErr(409, "REVISION_CONFLICT", "confirmation does not match the revision hash", "reload_revision")
	}
	if r.State == "confirmed" {
		return r, nil
	}
	now := s.now()
	if err := tx.Model(&r).Updates(map[string]any{"state": "confirmed", "confirmed_by": a.UserID, "confirmed_at": now}).Error; err != nil {
		return r, err
	}
	s.audit(tx, a, "problem.confirm", r.ID, r.InputHash)
	r.State, r.ConfirmedBy, r.ConfirmedAt = "confirmed", a.UserID, &now
	return r, nil
}

// ---------------------------------------------------------------------------
// Jobs

// StartAnalysis returns quickly with a job (FR-J01); the math runs in the isolated service.
func (s *Service) StartAnalysis(ctx context.Context, tx *gorm.DB, a Actor, revisionID, idemKey string) (model.EduMathJob, error) {
	r, _, err := s.revision(tx, a, revisionID)
	if err != nil {
		return model.EduMathJob{}, err
	}
	if r.State != "confirmed" {
		return model.EduMathJob{}, appErr(409, "INPUT_AMBIGUOUS", "problem revision is not confirmed", "teacher_confirm_input")
	}
	var problem map[string]any
	_ = json.Unmarshal([]byte(r.Structured), &problem)
	payload := map[string]any{"problem": problem, "problem_hash": r.InputHash}
	return s.startJob(ctx, tx, a, "analyze", idemKey, hashOf("analyze", r.ID, r.InputHash), r.ID, "", payload)
}

// StartLessonBuild requires a verified solution of the same revision.
func (s *Service) StartLessonBuild(ctx context.Context, tx *gorm.DB, a Actor, solutionID, idemKey string) (model.EduMathJob, error) {
	var sol model.EduSolutionRevision
	if err := tx.First(&sol, "id = ? AND tenant_id = ?", solutionID, a.TenantID).Error; err != nil {
		return model.EduMathJob{}, ErrNotFound
	}
	r, _, err := s.revision(tx, a, sol.ProblemRevisionID)
	if err != nil {
		return model.EduMathJob{}, err
	}
	if sol.MathState != "verified" {
		return model.EduMathJob{}, appErr(409, "VALIDATION_FAILED", "solution is not verified; lesson generation is blocked", "teacher_review")
	}
	var src model.EduMathJob
	if err := tx.First(&src, "id = ? AND tenant_id = ?", sol.JobID, a.TenantID).Error; err != nil {
		return model.EduMathJob{}, ErrNotFound
	}
	var problem map[string]any
	_ = json.Unmarshal([]byte(r.Structured), &problem)
	payload := map[string]any{"problem": problem, "problem_hash": r.InputHash, "analysis_job_id": src.ServiceJobID,
		"solution_hash": sol.SolutionHash, "report_hash": sol.ReportHash}
	return s.startJob(ctx, tx, a, "lesson_build", idemKey, hashOf("lesson_build", sol.ID, sol.SolutionHash), r.ID, src.ID, payload)
}

func (s *Service) startJob(ctx context.Context, tx *gorm.DB, a Actor, kind, idemKey, inputHash, revisionID, sourceJob string, payload map[string]any) (model.EduMathJob, error) {
	if !a.Has("teacher") {
		return model.EduMathJob{}, ErrNoRole
	}
	if idemKey == "" || len(idemKey) > 120 {
		return model.EduMathJob{}, appErr(400, "INPUT_AMBIGUOUS", "idempotencyKey is required (<=120 chars)", "retry_with_key")
	}
	var existing model.EduMathJob
	if err := tx.First(&existing, "tenant_id = ? AND kind = ? AND idem_key = ?", a.TenantID, kind, idemKey).Error; err == nil {
		if existing.InputHash != inputHash {
			return existing, appErr(409, "REVISION_CONFLICT", "idempotency key reused with a different input", "use_new_key")
		}
		return existing, nil
	}
	if !s.Client.Configured() {
		return model.EduMathJob{}, appErr(503, "PROVIDER_UNAVAILABLE", "math service is not configured", "contact_admin")
	}
	pb := flow.CurrentPriceBook()
	now := s.now()
	job := model.EduMathJob{ID: flow.NewID("emj_"), TenantID: a.TenantID, WorkspaceID: a.WorkspaceID, Kind: kind, IdemKey: idemKey, InputHash: inputHash,
		ProblemRevisionID: revisionID, SourceJobID: sourceJob, Status: "queued", PriceVersion: pb.Version, BudgetMicro: JobBudgetMicro(),
		Settlement: "open", CreatedBy: a.UserID, CreatedAt: now, UpdatedAt: now}
	if unitPrice(pb) > 0 {
		b := flow.Billing{DB: tx, Now: s.Now}
		rsv, err := b.Reserve(a.TenantID, "edu:job:"+job.ID, "edu_math_job", job.ID, job.BudgetMicro)
		if err != nil {
			if errors.Is(err, flow.ErrBudgetInsufficient) || errors.Is(err, flow.ErrBudgetQuota) || errors.Is(err, flow.ErrBudgetProvider) {
				return model.EduMathJob{}, appErr(402, "BUDGET_EXCEEDED", "insufficient budget for this math job; nothing was dispatched", "raise_budget")
			}
			return model.EduMathJob{}, err
		}
		job.ReservationID = rsv.ID
	}
	if err := tx.Create(&job).Error; err != nil {
		return job, err
	}
	s.audit(tx, a, "job.start", job.ID, kind)
	v, err := s.Client.StartJob(ctx, a.TenantID, a.UserID, kind, "edu:"+job.ID, job.ID, payload)
	if err != nil {
		var se *ServiceError
		if errors.As(err, &se) {
			job.Status, job.ErrorCode = "failed", se.Code
			raw, _ := json.Marshal(se)
			job.ErrorJSON = string(raw)
			tx.Model(&job).Updates(map[string]any{"status": job.Status, "error_code": job.ErrorCode, "error_json": job.ErrorJSON, "updated_at": s.now()})
			s.finishSettlement(tx, &job)
			return job, nil
		}
		// Transport failure: the service may have accepted it. Reconcile by key, never resend blindly.
		job.Status = "reconciliation_required"
		tx.Model(&job).Updates(map[string]any{"status": job.Status, "updated_at": s.now()})
		return job, nil
	}
	return s.apply(ctx, tx, job, v)
}

// Sync pulls the service's job state and applies it (idempotent; stale seq ignored).
func (s *Service) Sync(ctx context.Context, tx *gorm.DB, job model.EduMathJob) (model.EduMathJob, error) {
	switch job.Status {
	case "succeeded", "failed", "cancelled":
		if job.Settlement == "open" {
			s.finishSettlement(tx, &job)
		}
		return job, nil
	case "reconciliation_required":
		return s.Reconcile(ctx, tx, job)
	}
	v, err := s.Client.GetJob(ctx, job.TenantID, job.ServiceJobID)
	if err != nil {
		return job, nil // keep last known state; poll again later
	}
	return s.apply(ctx, tx, job, v)
}

// Reconcile resolves an unknown submit outcome by key (FR-J04).
func (s *Service) Reconcile(ctx context.Context, tx *gorm.DB, job model.EduMathJob) (model.EduMathJob, error) {
	if job.Status != "reconciliation_required" {
		return job, nil
	}
	found, v, err := s.Client.Reconcile(ctx, job.TenantID, job.Kind, "edu:"+job.ID)
	if err != nil {
		return job, nil // still unknown; stays held
	}
	if found {
		return s.apply(ctx, tx, job, v)
	}
	// Proven not executed: resubmitting under the same key is safe.
	var rev model.EduProblemRevision
	if err := tx.First(&rev, "id = ? AND tenant_id = ?", job.ProblemRevisionID, job.TenantID).Error; err != nil {
		return job, err
	}
	var problem map[string]any
	_ = json.Unmarshal([]byte(rev.Structured), &problem)
	payload := map[string]any{"problem": problem, "problem_hash": rev.InputHash}
	if job.Kind == "lesson_build" {
		var sol model.EduSolutionRevision
		var src model.EduMathJob
		if tx.First(&src, "id = ?", job.SourceJobID).Error != nil || tx.First(&sol, "job_id = ?", job.SourceJobID).Error != nil {
			return job, ErrNotFound
		}
		payload["analysis_job_id"], payload["solution_hash"], payload["report_hash"] = src.ServiceJobID, sol.SolutionHash, sol.ReportHash
	}
	v, err = s.Client.StartJob(ctx, job.TenantID, job.CreatedBy, job.Kind, "edu:"+job.ID, job.ID, payload)
	if err != nil {
		return job, nil
	}
	return s.apply(ctx, tx, job, v)
}

func (s *Service) Cancel(ctx context.Context, tx *gorm.DB, a Actor, job model.EduMathJob) (model.EduMathJob, error) {
	if !a.Has("teacher") {
		return job, ErrNoRole
	}
	tx.Model(&job).Updates(map[string]any{"cancel_requested": true, "updated_at": s.now()})
	job.CancelRequested = true
	if job.ServiceJobID == "" || job.Status == "succeeded" || job.Status == "failed" || job.Status == "cancelled" {
		return job, nil
	}
	v, err := s.Client.CancelJob(ctx, job.TenantID, job.ServiceJobID)
	if err != nil {
		return job, nil
	}
	s.audit(tx, a, "job.cancel", job.ID, job.Stage)
	return s.apply(ctx, tx, job, v)
}

func (s *Service) apply(ctx context.Context, tx *gorm.DB, job model.EduMathJob, v JobView) (model.EduMathJob, error) {
	if job.ServiceJobID != "" && job.ServiceJobID != v.JobID {
		return job, fmt.Errorf("%w: job id changed", ErrContract)
	}
	if job.ServiceJobID != "" && v.Seq <= job.Seq && job.Status == v.Status {
		return job, nil // duplicate or stale (FR-J03)
	}
	if v.Seq < job.Seq {
		return job, nil
	}
	upd := map[string]any{"service_job_id": v.JobID, "operation_id": v.OperationID, "status": v.Status, "stage": v.Stage, "progress": v.Progress,
		"seq": v.Seq, "updated_at": s.now()}
	if v.Error != nil {
		raw, _ := json.Marshal(v.Error)
		upd["error_code"], upd["error_json"] = v.Error.Code, string(raw)
	}
	res := tx.Model(&model.EduMathJob{}).Where("id = ? AND seq <= ?", job.ID, v.Seq).Updates(upd)
	if res.Error != nil {
		return job, res.Error
	}
	if err := tx.First(&job, "id = ?", job.ID).Error; err != nil {
		return job, err
	}
	for _, r := range v.ResultRefs {
		art := model.EduArtifact{ID: flow.NewID("ear_"), TenantID: job.TenantID, JobID: job.ID, ServiceArtifactID: r.ArtifactID, Kind: r.Kind,
			Audience: r.Audience, MediaType: r.MediaType, SHA256: r.SHA256, Size: r.Size, CreatedAt: s.now()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&art).Error; err != nil {
			return job, err
		}
	}
	if err := s.settleStages(tx, &job, v); err != nil {
		return job, err
	}
	if v.Status == "succeeded" {
		if err := s.materialize(ctx, tx, job); err != nil {
			return job, err
		}
	}
	if v.Status == "succeeded" || v.Status == "failed" || v.Status == "cancelled" {
		s.finishSettlement(tx, &job)
	}
	return job, nil
}

// settleStages charges each finished stage exactly once (unique (job, stage) row),
// as a child of the job reservation; usage lines are recorded without a second charge.
func (s *Service) settleStages(tx *gorm.DB, job *model.EduMathJob, v JobView) error {
	pb := flow.CurrentPriceBook()
	unit := unitPrice(pb)
	for _, u := range v.Usage {
		var st StageView
		for _, x := range v.Stages {
			if x.Name == u.Stage {
				st = x
			}
		}
		if st.Status != "succeeded" && st.Status != "failed" {
			continue
		}
		row := model.EduStageRun{ID: flow.NewID("esr_"), TenantID: job.TenantID, JobID: job.ID, Stage: u.Stage, Status: st.Status, ComputeMS: u.Quantity,
			CostMicro: u.Quantity * unit, CreatedAt: s.now()}
		ins := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if ins.Error != nil {
			return ins.Error
		}
		if ins.RowsAffected == 0 {
			continue // already settled for this stage
		}
		if job.ReservationID != "" && row.CostMicro > 0 {
			b := flow.Billing{DB: tx, Now: s.Now}
			var root model.FlowBudgetReservation
			if err := tx.First(&root, "id = ?", job.ReservationID).Error; err != nil {
				return err
			}
			amount := row.CostMicro
			if left := root.ReservedMicro - root.AllocatedMicro - root.ConsumedMicro; amount > left {
				amount = left // capped by the frozen budget; overage is not charged
			}
			if amount > 0 {
				child, err := b.Allocate(root.ID, "edu:stage:"+job.ID+":"+u.Stage, "edu_math_stage", job.ID+":"+u.Stage, amount)
				if err != nil {
					return err
				}
				if _, err := b.Settle(child.ID, amount); err != nil {
					return err
				}
				now := s.now()
				tx.Model(&row).Updates(map[string]any{"reservation_id": child.ID, "cost_micro": amount, "settled_at": now})
				row.ReservationID = child.ID
			}
		}
		if _, err := flow.RecordUsage(tx, flow.UsageInput{TenantID: job.TenantID, WorkspaceID: job.WorkspaceID, UsageType: flow.UsageMathComputeMS,
			SubjectRef: "edu_job:" + job.ID + ":" + u.Stage, IdemKey: "edu:stage:" + job.ID + ":" + u.Stage, ReservationID: row.ReservationID,
			Quantity: u.Quantity, CostMicro: row.CostMicro}, s.now()); err != nil {
			return err
		}
	}
	return nil
}

// finishSettlement closes the job's root reservation exactly once.
func (s *Service) finishSettlement(tx *gorm.DB, job *model.EduMathJob) {
	if job.Settlement != "open" {
		return
	}
	res := tx.Model(&model.EduMathJob{}).Where("id = ? AND settlement = ?", job.ID, "open").Update("settlement", "settled")
	if res.Error != nil || res.RowsAffected == 0 {
		return
	}
	job.Settlement = "settled"
	if job.ReservationID != "" {
		b := flow.Billing{DB: tx, Now: s.Now}
		_, _ = b.Settle(job.ReservationID, 0) // stage children already consumed; remainder refunded
	}
}

// materialize turns succeeded jobs into solution / lesson revisions.
func (s *Service) materialize(ctx context.Context, tx *gorm.DB, job model.EduMathJob) error {
	arts := map[string]model.EduArtifact{}
	var rows []model.EduArtifact
	tx.Where("job_id = ?", job.ID).Find(&rows)
	for _, r := range rows {
		arts[r.Kind] = r
	}
	switch job.Kind {
	case "analyze":
		var n int64
		tx.Model(&model.EduSolutionRevision{}).Where("job_id = ?", job.ID).Count(&n)
		if n > 0 {
			return nil
		}
		sa, ra := arts["solution"], arts["validation_report"]
		if sa.ID == "" || ra.ID == "" {
			return fmt.Errorf("%w: missing analysis artifacts", ErrContract)
		}
		raw, _, err := s.Client.Artifact(ctx, job.TenantID, sa.ServiceArtifactID, sa.SHA256)
		if err != nil {
			return err
		}
		var sol struct {
			SolutionStatus string `json:"solution_status"`
		}
		_ = json.Unmarshal(raw, &sol)
		rraw, _, err := s.Client.Artifact(ctx, job.TenantID, ra.ServiceArtifactID, ra.SHA256)
		if err != nil {
			return err
		}
		var rep struct {
			MathState      string   `json:"math_state"`
			EvidenceLevels []string `json:"evidence_levels"`
		}
		_ = json.Unmarshal(rraw, &rep)
		lv, _ := json.Marshal(rep.EvidenceLevels)
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.EduSolutionRevision{ID: flow.NewID("esv_"), TenantID: job.TenantID,
			ProblemRevisionID: job.ProblemRevisionID, JobID: job.ID, SolutionArtifact: sa.ID, SolutionHash: sa.SHA256, ReportArtifact: ra.ID,
			ReportHash: ra.SHA256, SolutionStatus: sol.SolutionStatus, MathState: rep.MathState, EvidenceLevels: string(lv), CreatedAt: s.now()}).Error
	case "lesson_build":
		var n int64
		tx.Model(&model.EduLessonRevision{}).Where("job_id = ?", job.ID).Count(&n)
		if n > 0 {
			return nil
		}
		la := arts["lesson"]
		if la.ID == "" || arts["lesson_student"].ID == "" || arts["preview_html"].ID == "" || arts["preview_html_student"].ID == "" {
			return fmt.Errorf("%w: missing lesson artifacts", ErrContract)
		}
		var sol model.EduSolutionRevision
		if err := tx.First(&sol, "job_id = ?", job.SourceJobID).Error; err != nil {
			return err
		}
		state, blocking := "ready_for_review", ""
		if sol.MathState != "verified" {
			state, blocking = "rejected", `["math_not_verified"]`
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.EduLessonRevision{ID: flow.NewID("elr_"), TenantID: job.TenantID,
			ProblemRevisionID: job.ProblemRevisionID, SolutionRevisionID: sol.ID, JobID: job.ID, LessonArtifact: la.ID, LessonHash: la.SHA256,
			StudentArtifact: arts["lesson_student"].ID, PreviewArtifact: arts["preview_html"].ID, StudentPreview: arts["preview_html_student"].ID,
			State: state, BlockingErrors: blocking, CreatedBy: job.CreatedBy, CreatedAt: s.now()}).Error
	}
	return nil
}

// ---------------------------------------------------------------------------
// Review and publication

func (s *Service) policy(tx *gorm.DB, tenantID string) string {
	var p model.EduOrgPolicy
	if tx.First(&p, "tenant_id = ?", tenantID).Error == nil && p.ReviewMode != "" {
		return p.ReviewMode
	}
	return "self_allowed"
}

// Review binds the decision to the exact lesson revision and hash (constraint 3, FR-I04).
func (s *Service) Review(tx *gorm.DB, a Actor, lessonID, lessonHash, decision, comment string) (model.EduReviewRecord, error) {
	var l model.EduLessonRevision
	if err := tx.First(&l, "id = ? AND tenant_id = ?", lessonID, a.TenantID).Error; err != nil {
		return model.EduReviewRecord{}, ErrNotFound
	}
	if decision != "approved" && decision != "rejected" {
		return model.EduReviewRecord{}, appErr(400, "INPUT_AMBIGUOUS", "decision must be approved or rejected", "")
	}
	if l.LessonHash != lessonHash {
		return model.EduReviewRecord{}, appErr(409, "REVISION_CONFLICT", "review must bind the current lesson hash", "reload_revision")
	}
	if l.State != "ready_for_review" {
		return model.EduReviewRecord{}, appErr(409, "REVISION_CONFLICT", "lesson revision is "+l.State, "reload_revision")
	}
	self := l.CreatedBy == a.UserID
	mode := s.policy(tx, a.TenantID)
	allowed := a.Has("reviewer") && !(self && mode == "second_reviewer")
	if !allowed && self && a.Has("teacher") && mode == "self_allowed" {
		allowed = true
	}
	if !allowed {
		return model.EduReviewRecord{}, appErr(403, "POLICY_REVOKED", "review not permitted by the institution review policy ("+mode+")", "request_reviewer")
	}
	if decision == "approved" && l.BlockingErrors != "" {
		return model.EduReviewRecord{}, appErr(409, "VALIDATION_FAILED", "lesson has blocking errors", "teacher_fix")
	}
	rec := model.EduReviewRecord{ID: flow.NewID("err_"), TenantID: a.TenantID, LessonRevisionID: l.ID, LessonHash: l.LessonHash, ReviewerID: a.UserID,
		SelfReview: self, Decision: decision, Comment: comment, CreatedAt: s.now()}
	newState := "approved"
	if decision == "rejected" {
		newState = "rejected"
	}
	res := tx.Model(&model.EduLessonRevision{}).Where("id = ? AND state = ? AND lesson_hash = ?", l.ID, "ready_for_review", lessonHash).Update("state", newState)
	if res.Error != nil {
		return rec, res.Error
	}
	if res.RowsAffected == 0 {
		return rec, appErr(409, "REVISION_CONFLICT", "lesson changed during review", "reload_revision")
	}
	if err := tx.Create(&rec).Error; err != nil {
		return rec, err
	}
	s.audit(tx, a, "lesson.review."+decision, l.ID, l.LessonHash)
	return rec, nil
}

// Publish creates an immutable publication bound to the approved hash.
func (s *Service) Publish(tx *gorm.DB, a Actor, lessonID, lessonHash string) (model.EduPublication, error) {
	var l model.EduLessonRevision
	if err := tx.First(&l, "id = ? AND tenant_id = ?", lessonID, a.TenantID).Error; err != nil {
		return model.EduPublication{}, ErrNotFound
	}
	if !a.Has("teacher") {
		return model.EduPublication{}, ErrNoRole
	}
	var existing model.EduPublication
	if tx.First(&existing, "lesson_revision_id = ?", l.ID).Error == nil {
		if existing.LessonHash != lessonHash {
			return existing, appErr(409, "REVISION_CONFLICT", "publication hash differs", "reload_revision")
		}
		return existing, nil
	}
	if l.State != "approved" || l.LessonHash != lessonHash || l.BlockingErrors != "" {
		return model.EduPublication{}, appErr(409, "VALIDATION_FAILED", "only an approved revision with the reviewed hash and no blocking errors can be published", "teacher_review")
	}
	var rec model.EduReviewRecord
	if err := tx.Where("lesson_revision_id = ? AND lesson_hash = ? AND decision = ?", l.ID, lessonHash, "approved").Order("created_at desc").First(&rec).Error; err != nil {
		return model.EduPublication{}, appErr(409, "VALIDATION_FAILED", "no approval bound to this hash", "teacher_review")
	}
	pub := model.EduPublication{ID: flow.NewID("epu_"), TenantID: a.TenantID, LessonRevisionID: l.ID, LessonHash: lessonHash, ReviewRecordID: rec.ID,
		State: "published", PublishedBy: a.UserID, CreatedAt: s.now()}
	if err := tx.Create(&pub).Error; err != nil {
		return pub, err
	}
	s.audit(tx, a, "lesson.publish", pub.ID, lessonHash)
	return pub, nil
}

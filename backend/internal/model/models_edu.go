package model

import "time"

// Education math workbench (docs/math-workbench). Every table carries tenant_id
// and is under FORCE RLS (database.flowRLSTables). Students are NOT workspace
// members: their access is derived from EduRoleBinding + EduEnrollment and is
// re-checked by the edumath application layer on every request.

// EduOrgPolicy holds per-tenant education policies (FR-I04/I05).
type EduOrgPolicy struct {
	TenantID   string `json:"tenantId" gorm:"primaryKey;size:40"`
	ReviewMode string `json:"reviewMode" gorm:"size:24;not null;default:self_allowed"` // self_allowed | second_reviewer
	// ConsentMode (FR-I05): guardian_required (default; empty is treated the same)
	// blocks every student learning access until an org admin records consent with
	// evidence; not_applicable is an explicit, audited admin decision (e.g. adults).
	ConsentMode string    `json:"consentMode" gorm:"size:24;not null;default:guardian_required"`
	UpdatedBy   string    `json:"updatedBy" gorm:"size:40"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (EduOrgPolicy) TableName() string { return "edu_org_policies" }

// EduRoleBinding binds an education role to a real tenant (and optionally a workspace).
type EduRoleBinding struct {
	ID          string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID    string    `json:"tenantId" gorm:"size:40;not null;uniqueIndex:ux_edu_role,priority:1;index"`
	UserID      string    `json:"userId" gorm:"size:40;not null;uniqueIndex:ux_edu_role,priority:2;index"`
	Role        string    `json:"role" gorm:"size:16;not null;uniqueIndex:ux_edu_role,priority:3"` // org_admin | reviewer | teacher | student
	WorkspaceID string    `json:"workspaceId,omitempty" gorm:"size:40"`
	Status      string    `json:"status" gorm:"size:16;not null;default:active"` // active | revoked
	CreatedBy   string    `json:"createdBy" gorm:"size:40"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (EduRoleBinding) TableName() string { return "edu_role_bindings" }

type EduClass struct {
	ID          string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID    string    `json:"tenantId" gorm:"size:40;not null;index"`
	WorkspaceID string    `json:"workspaceId" gorm:"size:40;not null;index"`
	Name        string    `json:"name" gorm:"size:120;not null"`
	Status      string    `json:"status" gorm:"size:16;not null;default:active"`
	CreatedBy   string    `json:"createdBy" gorm:"size:40"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (EduClass) TableName() string { return "edu_classes" }

type EduTeachingAssignment struct {
	ID        string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID  string    `json:"tenantId" gorm:"size:40;not null;index"`
	ClassID   string    `json:"classId" gorm:"size:40;not null;uniqueIndex:ux_edu_teach,priority:1"`
	UserID    string    `json:"userId" gorm:"size:40;not null;uniqueIndex:ux_edu_teach,priority:2;index"`
	Status    string    `json:"status" gorm:"size:16;not null;default:active"`
	CreatedAt time.Time `json:"createdAt"`
}

func (EduTeachingAssignment) TableName() string { return "edu_teaching_assignments" }

type EduEnrollment struct {
	ID            string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID      string    `json:"tenantId" gorm:"size:40;not null;index"`
	ClassID       string    `json:"classId" gorm:"size:40;not null;uniqueIndex:ux_edu_enroll,priority:1"`
	StudentUserID string    `json:"studentUserId" gorm:"size:40;not null;uniqueIndex:ux_edu_enroll,priority:2;index"`
	Status        string    `json:"status" gorm:"size:16;not null;default:active"` // active | withdrawn
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func (EduEnrollment) TableName() string { return "edu_enrollments" }

// EduClassInvite is a class join code. Only the SHA-256 of the secret is stored;
// the code handed to students is "<id>.<secret>" and is shown once.
type EduClassInvite struct {
	ID         string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID   string    `json:"tenantId" gorm:"size:40;not null;index"`
	ClassID    string    `json:"classId" gorm:"size:40;not null;index"`
	SecretHash string    `json:"-" gorm:"size:80;not null"`
	ExpiresAt  time.Time `json:"expiresAt"`
	MaxUses    int       `json:"maxUses"`
	Uses       int       `json:"uses"`
	Status     string    `json:"status" gorm:"size:16;not null;default:active"` // active | revoked
	CreatedBy  string    `json:"createdBy" gorm:"size:40"`
	CreatedAt  time.Time `json:"createdAt"`
}

func (EduClassInvite) TableName() string { return "edu_class_invites" }

// EduConsentRecord is the current guardian-consent state of one student in one
// institution (history lives in edu_audit_events). Recorded by an org admin with
// an evidence reference; a teacher's checkbox cannot create it (AC-04).
type EduConsentRecord struct {
	ID            string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID      string    `json:"tenantId" gorm:"size:40;not null;uniqueIndex:ux_edu_consent,priority:1"`
	StudentUserID string    `json:"studentUserId" gorm:"size:40;not null;uniqueIndex:ux_edu_consent,priority:2"`
	Kind          string    `json:"kind" gorm:"size:24;not null;uniqueIndex:ux_edu_consent,priority:3"` // guardian
	Status        string    `json:"status" gorm:"size:16;not null"`                                     // granted | withdrawn
	EvidenceRef   string    `json:"evidenceRef" gorm:"size:300;not null"`
	RecordedBy    string    `json:"recordedBy" gorm:"size:40;not null"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func (EduConsentRecord) TableName() string { return "edu_consent_records" }

type EduProblem struct {
	ID             string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID       string    `json:"tenantId" gorm:"size:40;not null;index"`
	WorkspaceID    string    `json:"workspaceId" gorm:"size:40;not null;index"`
	FamilyID       string    `json:"familyId" gorm:"size:40;not null"`
	Title          string    `json:"title" gorm:"size:200"`
	LatestRevision int       `json:"latestRevision"`
	CreatedBy      string    `json:"createdBy" gorm:"size:40"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func (EduProblem) TableName() string { return "edu_problems" }

// EduProblemRevision is immutable once confirmed; edits create a new revision.
type EduProblemRevision struct {
	ID          string     `json:"id" gorm:"primaryKey;size:40"`
	TenantID    string     `json:"tenantId" gorm:"size:40;not null;index"`
	ProblemID   string     `json:"problemId" gorm:"size:40;not null;uniqueIndex:ux_edu_prob_rev,priority:1"`
	Revision    int        `json:"revision" gorm:"not null;uniqueIndex:ux_edu_prob_rev,priority:2"`
	Structured  string     `json:"structured" gorm:"type:text;not null"`
	InputHash   string     `json:"inputHash" gorm:"size:80;not null"`
	State       string     `json:"state" gorm:"size:24;not null"` // needs_confirmation | confirmed
	ConfirmedBy string     `json:"confirmedBy,omitempty" gorm:"size:40"`
	ConfirmedAt *time.Time `json:"confirmedAt,omitempty"`
	CreatedBy   string     `json:"createdBy" gorm:"size:40"`
	CreatedAt   time.Time  `json:"createdAt"`
}

func (EduProblemRevision) TableName() string { return "edu_problem_revisions" }

// EduMathJob mirrors one logical math-service job (analyze | lesson_build).
type EduMathJob struct {
	ID                string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID          string    `json:"tenantId" gorm:"size:40;not null;uniqueIndex:ux_edu_job_idem,priority:1;index"`
	WorkspaceID       string    `json:"workspaceId" gorm:"size:40;not null"`
	Kind              string    `json:"kind" gorm:"size:24;not null;uniqueIndex:ux_edu_job_idem,priority:2"`
	IdemKey           string    `json:"idemKey" gorm:"size:160;not null;uniqueIndex:ux_edu_job_idem,priority:3"`
	InputHash         string    `json:"inputHash" gorm:"size:80;not null"`
	ProblemRevisionID string    `json:"problemRevisionId" gorm:"size:40;not null;index"`
	SourceJobID       string    `json:"sourceJobId,omitempty" gorm:"size:40"`
	ServiceJobID      string    `json:"serviceJobId,omitempty" gorm:"size:64;index"`
	OperationID       string    `json:"operationId,omitempty" gorm:"size:64"`
	Status            string    `json:"status" gorm:"size:32;not null;index"` // queued | running | succeeded | failed | cancelled | reconciliation_required
	Stage             string    `json:"stage" gorm:"size:40"`
	Progress          float64   `json:"progress"`
	Seq               int64     `json:"seq"`
	ErrorCode         string    `json:"errorCode,omitempty" gorm:"size:40"`
	ErrorJSON         string    `json:"errorJson,omitempty" gorm:"type:text"`
	PriceVersion      string    `json:"priceVersion" gorm:"size:40"`
	BudgetMicro       int64     `json:"budgetMicro"`
	ReservationID     string    `json:"reservationId,omitempty" gorm:"size:40"`
	Settlement        string    `json:"settlement" gorm:"size:24;not null;default:open"` // open | settled | released | held_unknown
	CancelRequested   bool      `json:"cancelRequested"`
	CreatedBy         string    `json:"createdBy" gorm:"size:40"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

func (EduMathJob) TableName() string { return "edu_math_jobs" }

// EduStageRun is settled exactly once per (job, stage).
type EduStageRun struct {
	ID            string     `json:"id" gorm:"primaryKey;size:40"`
	TenantID      string     `json:"tenantId" gorm:"size:40;not null;index"`
	JobID         string     `json:"jobId" gorm:"size:40;not null;uniqueIndex:ux_edu_stage,priority:1"`
	Stage         string     `json:"stage" gorm:"size:40;not null;uniqueIndex:ux_edu_stage,priority:2"`
	Status        string     `json:"status" gorm:"size:24;not null"`
	ComputeMS     int64      `json:"computeMs"`
	CostMicro     int64      `json:"costMicro"`
	ReservationID string     `json:"reservationId,omitempty" gorm:"size:40"`
	SettledAt     *time.Time `json:"settledAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
}

func (EduStageRun) TableName() string { return "edu_stage_runs" }

// EduArtifact references a private artifact in the math service store.
type EduArtifact struct {
	ID                string     `json:"id" gorm:"primaryKey;size:40"`
	TenantID          string     `json:"tenantId" gorm:"size:40;not null;index"`
	JobID             string     `json:"jobId" gorm:"size:40;not null;index"`
	ServiceArtifactID string     `json:"-" gorm:"size:64;not null;uniqueIndex"`
	Kind              string     `json:"kind" gorm:"size:40;not null"`
	Audience          string     `json:"audience" gorm:"size:16;not null"` // teacher | student
	MediaType         string     `json:"mediaType" gorm:"size:80"`
	SHA256            string     `json:"sha256" gorm:"size:80"`
	Size              int64      `json:"size"`
	RevokedAt         *time.Time `json:"revokedAt,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
}

func (EduArtifact) TableName() string { return "edu_artifacts" }

type EduSolutionRevision struct {
	ID                string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID          string    `json:"tenantId" gorm:"size:40;not null;index"`
	ProblemRevisionID string    `json:"problemRevisionId" gorm:"size:40;not null;index"`
	JobID             string    `json:"jobId" gorm:"size:40;not null;uniqueIndex"`
	SolutionArtifact  string    `json:"solutionArtifactId" gorm:"size:40"`
	SolutionHash      string    `json:"solutionHash" gorm:"size:80"`
	ReportArtifact    string    `json:"reportArtifactId" gorm:"size:40"`
	ReportHash        string    `json:"reportHash" gorm:"size:80"`
	SolutionStatus    string    `json:"solutionStatus" gorm:"size:24"`
	MathState         string    `json:"mathState" gorm:"size:32"`
	EvidenceLevels    string    `json:"evidenceLevels" gorm:"type:text"`
	CreatedAt         time.Time `json:"createdAt"`
}

func (EduSolutionRevision) TableName() string { return "edu_solution_revisions" }

type EduLessonRevision struct {
	ID                 string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID           string    `json:"tenantId" gorm:"size:40;not null;index"`
	ProblemRevisionID  string    `json:"problemRevisionId" gorm:"size:40;not null;index"`
	SolutionRevisionID string    `json:"solutionRevisionId" gorm:"size:40;not null"`
	JobID              string    `json:"jobId" gorm:"size:40;not null;uniqueIndex"`
	LessonArtifact     string    `json:"lessonArtifactId" gorm:"size:40"`
	LessonHash         string    `json:"lessonHash" gorm:"size:80;not null"`
	StudentArtifact    string    `json:"studentArtifactId" gorm:"size:40"`
	PreviewArtifact    string    `json:"previewArtifactId" gorm:"size:40"`
	StudentPreview     string    `json:"studentPreviewArtifactId" gorm:"size:40"`
	State              string    `json:"state" gorm:"size:24;not null"` // ready_for_review | approved | rejected | superseded
	BlockingErrors     string    `json:"blockingErrors,omitempty" gorm:"type:text"`
	CreatedBy          string    `json:"createdBy" gorm:"size:40"`
	CreatedAt          time.Time `json:"createdAt"`
}

func (EduLessonRevision) TableName() string { return "edu_lesson_revisions" }

type EduReviewRecord struct {
	ID               string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID         string    `json:"tenantId" gorm:"size:40;not null;index"`
	LessonRevisionID string    `json:"lessonRevisionId" gorm:"size:40;not null;index"`
	LessonHash       string    `json:"lessonHash" gorm:"size:80;not null"`
	ReviewerID       string    `json:"reviewerId" gorm:"size:40;not null"`
	SelfReview       bool      `json:"selfReview"`
	Decision         string    `json:"decision" gorm:"size:16;not null"` // approved | rejected
	Comment          string    `json:"comment" gorm:"size:1000"`
	CreatedAt        time.Time `json:"createdAt"`
}

func (EduReviewRecord) TableName() string { return "edu_review_records" }

// EduPublication is immutable content-wise; only its state changes (withdrawn/archived).
type EduPublication struct {
	ID               string     `json:"id" gorm:"primaryKey;size:40"`
	TenantID         string     `json:"tenantId" gorm:"size:40;not null;index"`
	LessonRevisionID string     `json:"lessonRevisionId" gorm:"size:40;not null;uniqueIndex"`
	LessonHash       string     `json:"lessonHash" gorm:"size:80;not null"`
	ReviewRecordID   string     `json:"reviewRecordId" gorm:"size:40;not null"`
	State            string     `json:"state" gorm:"size:16;not null"` // published | withdrawn | archived
	PublishedBy      string     `json:"publishedBy" gorm:"size:40"`
	CreatedAt        time.Time  `json:"createdAt"`
	ChangedAt        *time.Time `json:"changedAt,omitempty"`
}

func (EduPublication) TableName() string { return "edu_publications" }

// EduAssignment releases a publication to a class (the task audience).
type EduAssignment struct {
	ID            string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID      string    `json:"tenantId" gorm:"size:40;not null;index"`
	ClassID       string    `json:"classId" gorm:"size:40;not null;index"`
	PublicationID string    `json:"publicationId" gorm:"size:40;not null;index"`
	Title         string    `json:"title" gorm:"size:200"`
	State         string    `json:"state" gorm:"size:16;not null"` // active | closed | withdrawn
	CreatedBy     string    `json:"createdBy" gorm:"size:40"`
	CreatedAt     time.Time `json:"createdAt"`
	// Task policy (FR-S01..S08, AC-21). Times are stored in UTC and compared on the server.
	StartsAt         *time.Time `json:"startsAt,omitempty"`
	DueAt            *time.Time `json:"dueAt,omitempty"`
	AllowLate        bool       `json:"allowLate" gorm:"not null;default:false"`
	AnswerPolicy     string     `json:"answerPolicy" gorm:"size:24;not null;default:teacher_release"` // after_submission | after_due | teacher_release
	AnswerReleasedAt *time.Time `json:"answerReleasedAt,omitempty"`
	ApproxAllowed    bool       `json:"approxAllowed" gorm:"not null;default:false"`
	UnitPolicy       string     `json:"unitPolicy" gorm:"size:16;not null;default:required"` // required | optional
}

func (EduAssignment) TableName() string { return "edu_assignments" }

// EduAttempt is one student response. The first valid main attempt is frozen
// (FirstValid); later attempts never overwrite it. Item: main | check.
type EduAttempt struct {
	ID            string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID      string    `json:"tenantId" gorm:"size:40;not null;index;uniqueIndex:ux_edu_attempt_key,priority:1"`
	AssignmentID  string    `json:"assignmentId" gorm:"size:40;not null;index:ix_edu_attempt_as,priority:1"`
	StudentUserID string    `json:"studentUserId" gorm:"size:40;not null;index:ix_edu_attempt_as,priority:2;uniqueIndex:ux_edu_attempt_key,priority:2"`
	ClientKey     string    `json:"clientKey" gorm:"size:64;not null;uniqueIndex:ux_edu_attempt_key,priority:3"`
	Item          string    `json:"item" gorm:"size:12;not null"` // main | check
	Kind          string    `json:"kind" gorm:"size:12;not null"` // answer | no_idea
	Response      string    `json:"response" gorm:"size:400"`
	Valid         bool      `json:"valid"`
	InvalidReason string    `json:"invalidReason,omitempty" gorm:"size:40"`
	Result        string    `json:"result" gorm:"size:24"` // correct | correct_approx | incorrect | needs_exact | needs_teacher | undetermined | no_idea | invalid
	Reason        string    `json:"reason,omitempty" gorm:"size:60"`
	Grader        string    `json:"grader,omitempty" gorm:"size:40"`
	FirstValid    bool      `json:"firstValid"`
	Independent   bool      `json:"independent"`
	HintLevel     int       `json:"hintLevel"` // highest hint level unlocked before this attempt
	Late          bool      `json:"late"`
	ReviewItemID  string    `json:"reviewItemId,omitempty" gorm:"size:40"`
	TeacherResult string    `json:"teacherResult,omitempty" gorm:"size:24"`
	TeacherNote   string    `json:"teacherNote,omitempty" gorm:"size:500"`
	TeacherBy     string    `json:"teacherBy,omitempty" gorm:"size:40"`
	CreatedAt     time.Time `json:"createdAt"`
}

func (EduAttempt) TableName() string { return "edu_attempts" }

// EduHintUnlock records each hint level a student opened (FR-S05). Level 5 is the full solution.
type EduHintUnlock struct {
	ID            string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID      string    `json:"tenantId" gorm:"size:40;not null;index"`
	AssignmentID  string    `json:"assignmentId" gorm:"size:40;not null;uniqueIndex:ux_edu_hint,priority:1"`
	StudentUserID string    `json:"studentUserId" gorm:"size:40;not null;uniqueIndex:ux_edu_hint,priority:2"`
	Level         int       `json:"level" gorm:"not null;uniqueIndex:ux_edu_hint,priority:3"`
	CreatedAt     time.Time `json:"createdAt"`
}

func (EduHintUnlock) TableName() string { return "edu_hint_unlocks" }

// EduSelfTest is the independent self-test on the check question (FR-S08).
type EduSelfTest struct {
	ID            string     `json:"id" gorm:"primaryKey;size:40"`
	TenantID      string     `json:"tenantId" gorm:"size:40;not null;index"`
	AssignmentID  string     `json:"assignmentId" gorm:"size:40;not null;uniqueIndex:ux_edu_selftest,priority:1"`
	StudentUserID string     `json:"studentUserId" gorm:"size:40;not null;uniqueIndex:ux_edu_selftest,priority:2"`
	State         string     `json:"state" gorm:"size:16;not null"` // started | submitted
	HelpUsed      bool       `json:"helpUsed"`
	SawSolution   bool       `json:"sawSolution"` // the main solution was opened before the self-test started
	AttemptID     string     `json:"attemptId,omitempty" gorm:"size:40"`
	StartedAt     time.Time  `json:"startedAt"`
	SubmittedAt   *time.Time `json:"submittedAt,omitempty"`
}

func (EduSelfTest) TableName() string { return "edu_self_tests" }

// EduReviewItem schedules spaced review (FR-S09): +1, +3, +7 days; completion needs a new attempt.
type EduReviewItem struct {
	ID            string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID      string    `json:"tenantId" gorm:"size:40;not null;index"`
	AssignmentID  string    `json:"assignmentId" gorm:"size:40;not null;uniqueIndex:ux_edu_review_item,priority:1"`
	StudentUserID string    `json:"studentUserId" gorm:"size:40;not null;uniqueIndex:ux_edu_review_item,priority:2;index"`
	Reason        string    `json:"reason" gorm:"size:24;not null"` // incorrect | no_idea | heavy_hints
	Stage         int       `json:"stage"`                          // 1, 2, 3
	DueAt         time.Time `json:"dueAt" gorm:"index"`
	State         string    `json:"state" gorm:"size:16;not null"` // pending | done
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func (EduReviewItem) TableName() string { return "edu_review_items" }

type EduAuditEvent struct {
	ID        string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID  string    `json:"tenantId" gorm:"size:40;not null;index"`
	ActorID   string    `json:"actorId" gorm:"size:40"`
	Action    string    `json:"action" gorm:"size:60;not null"`
	Subject   string    `json:"subject" gorm:"size:80"`
	Detail    string    `json:"detail" gorm:"size:1000"`
	CreatedAt time.Time `json:"createdAt" gorm:"index"`
}

func (EduAuditEvent) TableName() string { return "edu_audit_events" }

// EduModels lists every education model for migration.
func EduModels() []any {
	return []any{&EduOrgPolicy{}, &EduRoleBinding{}, &EduClass{}, &EduTeachingAssignment{}, &EduEnrollment{}, &EduProblem{},
		&EduProblemRevision{}, &EduMathJob{}, &EduStageRun{}, &EduArtifact{}, &EduSolutionRevision{}, &EduLessonRevision{},
		&EduReviewRecord{}, &EduPublication{}, &EduAssignment{}, &EduAuditEvent{}, &EduClassInvite{}, &EduConsentRecord{},
		&EduAttempt{}, &EduHintUnlock{}, &EduSelfTest{}, &EduReviewItem{}}
}

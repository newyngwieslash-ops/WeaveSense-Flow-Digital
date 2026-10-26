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
	ConsentMode string `json:"consentMode" gorm:"size:24;not null;default:guardian_required"`
	// EditReviewInherit (FR-T06): none (default; empty is treated the same) makes every edited
	// lesson revision go through review again; pronunciation_only lets a pronunciation-only edit
	// of an approved revision inherit that approval (recorded, never shown as a new review).
	EditReviewInherit string    `json:"editReviewInherit" gorm:"size:24;not null;default:none"`
	UpdatedBy         string    `json:"updatedBy" gorm:"size:40"`
	UpdatedAt         time.Time `json:"updatedAt"`
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
	// ExpiresAt ends the binding without a revoke (DEV-013); nil means no end date.
	// A non-empty WorkspaceID scopes a staff role to that workspace only.
	ExpiresAt *time.Time `json:"expiresAt,omitempty" gorm:"index"`
	RevokedBy string     `json:"revokedBy,omitempty" gorm:"size:40"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
	Reason    string     `json:"reason,omitempty" gorm:"size:200"`
}

func (EduRoleBinding) TableName() string { return "edu_role_bindings" }

type EduClass struct {
	ID          string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID    string    `json:"tenantId" gorm:"size:40;not null;index"`
	WorkspaceID string    `json:"workspaceId" gorm:"size:40;not null;index"`
	Name        string    `json:"name" gorm:"size:120;not null"`
	Status      string    `json:"status" gorm:"size:16;not null;default:active"`
	CourseID    string    `json:"courseId,omitempty" gorm:"size:40;index"`
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
	SourceAssetID  string    `json:"sourceAssetId,omitempty" gorm:"size:40;index"` // DEV-026 rights record; empty = not registered
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
	// Options holds kind specific input that is not derivable from the revision (variant_set: {"count":n}),
	// kept so a proven not executed submit can be resent under the same key with the same input.
	Options string `json:"options,omitempty" gorm:"size:200"`
	// PriceBook is the price book frozen when the job was accepted (JSON); every stage settles with it (DEV-061).
	PriceBook string `json:"-" gorm:"type:text"`
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
	// Lineage of structured edits (FR-T05/T06). Empty for revisions built by lesson_build.
	ParentLessonRevisionID string `json:"parentLessonRevisionId,omitempty" gorm:"size:40;index"`
	EditClasses            string `json:"editClasses,omitempty" gorm:"size:80"`
	ApprovalInheritedFrom  string `json:"approvalInheritedFrom,omitempty" gorm:"size:40"`
}

func (EduLessonRevision) TableName() string { return "edu_lesson_revisions" }

// EduLessonEdit is one structured edit request on an exact base revision and hash. HeadKey
// is unique: a normal save takes "head:<base id>", so a second concurrent save of the same
// base is refused with the first one's details (AC-17); "save as branch" takes its own key.
// A failed or cancelled edit releases the head lazily when the next save arrives.
type EduLessonEdit struct {
	ID                     string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID               string    `json:"tenantId" gorm:"size:40;not null;index"`
	BaseLessonRevisionID   string    `json:"baseLessonRevisionId" gorm:"size:40;not null;index"`
	BaseLessonHash         string    `json:"baseLessonHash" gorm:"size:80;not null"`
	JobID                  string    `json:"jobId" gorm:"size:40;uniqueIndex"`
	HeadKey                string    `json:"-" gorm:"size:96;not null;uniqueIndex"`
	Branch                 bool      `json:"branch"`
	EditsJSON              string    `json:"-" gorm:"type:text;not null"`
	Classes                string    `json:"classes" gorm:"size:80"`
	State                  string    `json:"state" gorm:"size:16;not null"` // running | done | failed
	ResultLessonRevisionID string    `json:"resultLessonRevisionId,omitempty" gorm:"size:40"`
	CreatedBy              string    `json:"createdBy" gorm:"size:40"`
	CreatedAt              time.Time `json:"createdAt"`
}

func (EduLessonEdit) TableName() string { return "edu_lesson_edits" }

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
	// InheritedFrom is set only when an edit inherited the parent's approval under the explicit
	// institution rule (pronunciation_only); it names the parent review record and is never a new review.
	InheritedFrom string `json:"inheritedFrom,omitempty" gorm:"size:40"`
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
	// VariantSetID, when set, makes the approved variant set the self-test measurement unit (FR-S08, AC-22).
	// Fixed at creation; never swapped on a live assignment (FR-T09).
	VariantSetID string `json:"variantSetId,omitempty" gorm:"size:40"`
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

// EduHelpRequest is a private student request (FR-S11): a content error report,
// "I don't understand", or a question to the class teacher. GroupKey merges
// requests about the same assignment, kind and beat (FR-T13).
type EduHelpRequest struct {
	ID            string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID      string    `json:"tenantId" gorm:"size:40;not null;index;uniqueIndex:ux_edu_help_key,priority:1"`
	ClassID       string    `json:"classId" gorm:"size:40;not null;index"`
	AssignmentID  string    `json:"assignmentId" gorm:"size:40;not null;index"`
	StudentUserID string    `json:"studentUserId" gorm:"size:40;not null;index;uniqueIndex:ux_edu_help_key,priority:2"`
	ClientKey     string    `json:"clientKey" gorm:"size:64;not null;uniqueIndex:ux_edu_help_key,priority:3"`
	Kind          string    `json:"kind" gorm:"size:24;not null"` // content_error | access_problem | dont_understand | ask_teacher
	BeatID        string    `json:"beatId,omitempty" gorm:"size:40"`
	Message       string    `json:"message" gorm:"size:500"`
	GroupKey      string    `json:"groupKey" gorm:"size:120;not null;index"`
	Priority      int       `json:"priority"`                      // 1 content/access, 2 learning question
	State         string    `json:"state" gorm:"size:16;not null"` // open | answered
	ReplyID       string    `json:"replyId,omitempty" gorm:"size:40"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func (EduHelpRequest) TableName() string { return "edu_help_requests" }

// EduHelpReply answers a group of requests, privately or as a public class note
// (public notes must not contain personal identifiers).
type EduHelpReply struct {
	ID           string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID     string    `json:"tenantId" gorm:"size:40;not null;index"`
	ClassID      string    `json:"classId" gorm:"size:40;not null;index"`
	AssignmentID string    `json:"assignmentId" gorm:"size:40;not null;index"`
	GroupKey     string    `json:"groupKey" gorm:"size:120;not null"`
	Body         string    `json:"body" gorm:"size:2000;not null"`
	Public       bool      `json:"public"`
	AuthorID     string    `json:"authorId" gorm:"size:40;not null"`
	CreatedAt    time.Time `json:"createdAt"`
}

func (EduHelpReply) TableName() string { return "edu_help_replies" }

// EduPrediction is a student's qualitative prediction made before exploring an
// interactive figure (FR-S05). The first choice is kept; it is learning
// telemetry, never evidence of mastery or of mathematical correctness.
type EduPrediction struct {
	ID            string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID      string    `json:"tenantId" gorm:"size:40;not null;index;uniqueIndex:ux_edu_prediction,priority:1"`
	AssignmentID  string    `json:"assignmentId" gorm:"size:40;not null;index;uniqueIndex:ux_edu_prediction,priority:2"`
	StudentUserID string    `json:"studentUserId" gorm:"size:40;not null;index;uniqueIndex:ux_edu_prediction,priority:3"`
	PromptID      string    `json:"promptId" gorm:"size:40;not null;uniqueIndex:ux_edu_prediction,priority:4"`
	Choice        int       `json:"choice"`
	ChoiceText    string    `json:"choiceText" gorm:"size:200"`
	BeforeAttempt bool      `json:"beforeAttempt"`
	CreatedAt     time.Time `json:"createdAt"`
}

func (EduPrediction) TableName() string { return "edu_predictions" }

// EduFigureInteraction aggregates how a student used an interactive figure
// (counts only, capped per report). It is never used as learning evidence.
type EduFigureInteraction struct {
	ID            string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID      string    `json:"tenantId" gorm:"size:40;not null;index;uniqueIndex:ux_edu_figure_use,priority:1"`
	AssignmentID  string    `json:"assignmentId" gorm:"size:40;not null;index;uniqueIndex:ux_edu_figure_use,priority:2"`
	StudentUserID string    `json:"studentUserId" gorm:"size:40;not null;index;uniqueIndex:ux_edu_figure_use,priority:3"`
	Drags         int       `json:"drags"`
	KeyPresses    int       `json:"keyPresses"`
	FormInputs    int       `json:"formInputs"`
	Resets        int       `json:"resets"`
	Fallbacks     int       `json:"fallbacks"`
	Reports       int       `json:"reports"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func (EduFigureInteraction) TableName() string { return "edu_figure_interactions" }

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
		&EduAttempt{}, &EduHintUnlock{}, &EduSelfTest{}, &EduReviewItem{}, &EduHelpRequest{}, &EduHelpReply{},
		&EduPrediction{}, &EduFigureInteraction{}, &EduVariantSet{}, &EduVariantReview{}, &EduAssignmentExtension{}, &EduLessonEdit{}, &EduLedgerAnomaly{}, &EduJobEvent{}, &EduMathEvent{}, &EduBudget{}, &EduBudgetAlert{}, &EduSupportCost{}, &EduContentIncident{}, &EduAssignmentMigration{}, &EduSourceAsset{}, &EduRegrade{}, &EduCourse{}, &EduCourseAlignment{}, &EduRosterEntry{}}
}

// EduVariantSet is a finite set of numbers only variants of a lesson's problem (FR-M07, FR-T11, AC-22).
// Every item was independently re-solved and verified by the math service; it reaches students only after
// a review bound to SetHash approves it and an assignment references it. AnswerKeys never leaves staff APIs.
type EduVariantSet struct {
	ID                string     `json:"id" gorm:"primaryKey;size:40"`
	TenantID          string     `json:"tenantId" gorm:"size:40;not null;index"`
	WorkspaceID       string     `json:"workspaceId" gorm:"size:40;not null"`
	LessonRevisionID  string     `json:"lessonRevisionId" gorm:"size:40;not null;index"`
	ProblemRevisionID string     `json:"problemRevisionId" gorm:"size:40;not null"`
	JobID             string     `json:"jobId" gorm:"size:40;not null;uniqueIndex"`
	ArtifactID        string     `json:"artifactId" gorm:"size:40"`
	StudentArtifactID string     `json:"studentArtifactId" gorm:"size:40"`
	SetHash           string     `json:"setHash" gorm:"size:80;not null"`
	FamilyID          string     `json:"familyId" gorm:"size:40"`
	Requested         int        `json:"requested"`
	ItemCount         int        `json:"itemCount"`
	ItemsJSON         string     `json:"-" gorm:"type:text"` // public item fields (prompt, params, change type, evidence)
	AnswerKeysJSON    string     `json:"-" gorm:"type:text"` // teacher only
	DroppedJSON       string     `json:"-" gorm:"type:text"`
	State             string     `json:"state" gorm:"size:24;not null"` // ready_for_review | approved | rejected
	BlockingErrors    string     `json:"blockingErrors,omitempty" gorm:"size:400"`
	CreatedBy         string     `json:"createdBy" gorm:"size:40"`
	CreatedAt         time.Time  `json:"createdAt"`
	ReviewedAt        *time.Time `json:"reviewedAt,omitempty"`
}

func (EduVariantSet) TableName() string { return "edu_variant_sets" }

// EduVariantReview binds a decision to the exact set hash (constraint 3).
type EduVariantReview struct {
	ID           string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID     string    `json:"tenantId" gorm:"size:40;not null;index"`
	VariantSetID string    `json:"variantSetId" gorm:"size:40;not null;index"`
	SetHash      string    `json:"setHash" gorm:"size:80;not null"`
	ReviewerID   string    `json:"reviewerId" gorm:"size:40;not null"`
	SelfReview   bool      `json:"selfReview"`
	Decision     string    `json:"decision" gorm:"size:16;not null"`
	Comment      string    `json:"comment" gorm:"size:1000"`
	CreatedAt    time.Time `json:"createdAt"`
}

func (EduVariantReview) TableName() string { return "edu_variant_reviews" }

// EduAssignmentExtension moves one student's due time (AC-21). Stored in UTC; the server computes
// the effective due time for writes and for after_due answer release.
type EduAssignmentExtension struct {
	ID            string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID      string    `json:"tenantId" gorm:"size:40;not null;uniqueIndex:ux_edu_extension,priority:1"`
	AssignmentID  string    `json:"assignmentId" gorm:"size:40;not null;uniqueIndex:ux_edu_extension,priority:2"`
	StudentUserID string    `json:"studentUserId" gorm:"size:40;not null;uniqueIndex:ux_edu_extension,priority:3"`
	DueAt         time.Time `json:"dueAt"`
	Reason        string    `json:"reason" gorm:"size:200"`
	CreatedBy     string    `json:"createdBy" gorm:"size:40"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func (EduAssignmentExtension) TableName() string { return "edu_assignment_extensions" }

// EduLedgerAnomaly is one finding of the education ledger reconciliation (DEV-063):
// a job whose platform ledger, stage runs and usage lines do not agree. Unique per
// (tenant, job, kind); it carries an owner and a handling status until resolved.
type EduLedgerAnomaly struct {
	ID         string     `json:"id" gorm:"primaryKey;size:40"`
	TenantID   string     `json:"tenantId" gorm:"size:40;not null;uniqueIndex:ux_edu_ledger_anom,priority:1;index"`
	JobID      string     `json:"jobId" gorm:"size:40;not null;uniqueIndex:ux_edu_ledger_anom,priority:2"`
	Kind       string     `json:"kind" gorm:"size:40;not null;uniqueIndex:ux_edu_ledger_anom,priority:3"`
	Detail     string     `json:"detail" gorm:"size:400"`
	Status     string     `json:"status" gorm:"size:24;not null;index"` // open | investigating | resolved
	Owner      string     `json:"owner,omitempty" gorm:"size:40"`
	Note       string     `json:"note,omitempty" gorm:"size:400"`
	DetectedAt time.Time  `json:"detectedAt"`
	LastSeenAt time.Time  `json:"lastSeenAt"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
	ResolvedBy string     `json:"resolvedBy,omitempty" gorm:"size:40"`
}

func (EduLedgerAnomaly) TableName() string { return "edu_ledger_anomalies" }

// EduJobEvent is the platform outbox of job facts (DEV-057): written in the same
// transaction as the state change it describes, numbered per job without gaps,
// so a consumer that reads after its last seq after any restart or delay sees
// every fact exactly once and in order.
type EduJobEvent struct {
	ID         string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID   string    `json:"tenantId" gorm:"size:40;not null;index"`
	JobID      string    `json:"jobId" gorm:"size:40;not null;uniqueIndex:ux_edu_job_event_seq,priority:1"`
	Seq        int64     `json:"seq" gorm:"not null;uniqueIndex:ux_edu_job_event_seq,priority:2"`
	Type       string    `json:"type" gorm:"size:40;not null"` // job.accepted | job.<status> | job.settled | job.released
	Status     string    `json:"status" gorm:"size:32"`
	Settlement string    `json:"settlement" gorm:"size:16"`
	ServiceSeq int64     `json:"serviceSeq"`
	Detail     string    `json:"detail,omitempty" gorm:"size:200"`
	CreatedAt  time.Time `json:"createdAt"`
}

func (EduJobEvent) TableName() string { return "edu_job_events" }

// EduMathEvent is the inbox of signed completion events pushed by the math
// service (DEV-056). The event id is the dedupe key; events are hints only and
// the job is always re-read from the service before anything changes.
type EduMathEvent struct {
	ID           string    `json:"id" gorm:"primaryKey;size:40"` // service event id (me_…)
	TenantID     string    `json:"tenantId" gorm:"size:40;not null;index"`
	JobID        string    `json:"jobId" gorm:"size:40;index"`
	ServiceJobID string    `json:"serviceJobId" gorm:"size:40"`
	Seq          int64     `json:"seq"`
	Status       string    `json:"status" gorm:"size:32"`
	Result       string    `json:"result" gorm:"size:24"` // applied | stale | unknown_job | sync_failed
	ReceivedAt   time.Time `json:"receivedAt"`
}

func (EduMathEvent) TableName() string { return "edu_math_events" }

// EduBudget holds an organization's math-workbench spending and fairness limits
// (DEV-064). Zero means no limit. Amounts are micro units of the platform ledger.
type EduBudget struct {
	TenantID            string    `json:"tenantId" gorm:"primaryKey;size:40"`
	MonthlyMicro        int64     `json:"monthlyMicro"`
	TeacherMonthlyMicro int64     `json:"teacherMonthlyMicro"`
	MaxActiveJobs       int       `json:"maxActiveJobs"`
	MaxActivePerTeacher int       `json:"maxActivePerTeacher"`
	MaxRenderJobs       int       `json:"maxRenderJobs"`
	AlertPercent        int       `json:"alertPercent"`
	UpdatedBy           string    `json:"updatedBy" gorm:"size:64"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

func (EduBudget) TableName() string { return "edu_budgets" }

// EduBudgetAlert is raised once per period, scope and level (warn | exhausted).
type EduBudgetAlert struct {
	ID         string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID   string    `json:"tenantId" gorm:"size:40;not null;uniqueIndex:ux_edu_budget_alert,priority:1"`
	Period     string    `json:"period" gorm:"size:7;not null;uniqueIndex:ux_edu_budget_alert,priority:2"`
	Scope      string    `json:"scope" gorm:"size:80;not null;uniqueIndex:ux_edu_budget_alert,priority:3"`
	Level      string    `json:"level" gorm:"size:16;not null;uniqueIndex:ux_edu_budget_alert,priority:4"`
	SpentMicro int64     `json:"spentMicro"`
	LimitMicro int64     `json:"limitMicro"`
	CreatedAt  time.Time `json:"createdAt"`
}

func (EduBudgetAlert) TableName() string { return "edu_budget_alerts" }

// EduSupportCost is human support time attributed to a tenant and period for
// full delivery cost accounting (DEV-065); RecordedBy and the optional subject
// carry the attribution.
type EduSupportCost struct {
	ID         string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID   string    `json:"tenantId" gorm:"size:40;not null;index:ix_edu_support_cost,priority:1"`
	Period     string    `json:"period" gorm:"size:7;not null;index:ix_edu_support_cost,priority:2"`
	Category   string    `json:"category" gorm:"size:24;not null"`
	Minutes    int       `json:"minutes"`
	CostMicro  int64     `json:"costMicro"`
	SubjectRef string    `json:"subjectRef" gorm:"size:80"`
	Note       string    `json:"note" gorm:"size:200"`
	RecordedBy string    `json:"recordedBy" gorm:"size:64;not null"`
	CreatedAt  time.Time `json:"createdAt"`
}

func (EduSupportCost) TableName() string { return "edu_support_costs" }

// EduContentIncident records a content error found after publication (DEV-027,
// AC-34). A blocking incident withdraws the publication, suspends its
// assignments and revokes student media in one transaction; the impact
// snapshot lists everything derived from the revision.
type EduContentIncident struct {
	ID               string     `json:"id" gorm:"primaryKey;size:40"`
	TenantID         string     `json:"tenantId" gorm:"size:40;not null;index"`
	WorkspaceID      string     `json:"workspaceId" gorm:"size:40;not null"`
	PublicationID    string     `json:"publicationId" gorm:"size:40;not null;index"`
	LessonRevisionID string     `json:"lessonRevisionId" gorm:"size:40;not null;index"`
	Severity         string     `json:"severity" gorm:"size:16;not null"` // blocking | minor
	Reason           string     `json:"reason" gorm:"size:400;not null"`
	State            string     `json:"state" gorm:"size:16;not null"` // open | resolved
	ImpactJSON       string     `json:"-" gorm:"type:text"`
	FixPublicationID string     `json:"fixPublicationId,omitempty" gorm:"size:40"`
	ResolutionNote   string     `json:"resolutionNote,omitempty" gorm:"size:400"`
	CreatedBy        string     `json:"createdBy" gorm:"size:64;not null"`
	CreatedAt        time.Time  `json:"createdAt"`
	ResolvedBy       string     `json:"resolvedBy,omitempty" gorm:"size:64"`
	ResolvedAt       *time.Time `json:"resolvedAt,omitempty"`
}

func (EduContentIncident) TableName() string { return "edu_content_incidents" }

// EduAssignmentMigration moves a class task to a new publication (DEV-025). The
// old assignment and its attempts stay untouched in their original context.
type EduAssignmentMigration struct {
	ID               string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID         string    `json:"tenantId" gorm:"size:40;not null;index"`
	FromAssignmentID string    `json:"fromAssignmentId" gorm:"size:40;not null;uniqueIndex"`
	ToAssignmentID   string    `json:"toAssignmentId" gorm:"size:40;not null"`
	FromPublication  string    `json:"fromPublicationId" gorm:"size:40;not null"`
	ToPublication    string    `json:"toPublicationId" gorm:"size:40;not null"`
	IncidentID       string    `json:"incidentId,omitempty" gorm:"size:40"`
	Reason           string    `json:"reason" gorm:"size:400;not null"`
	AffectedStudents int       `json:"affectedStudents"`
	AttemptsKept     int       `json:"attemptsKept"`
	CreatedBy        string    `json:"createdBy" gorm:"size:64;not null"`
	CreatedAt        time.Time `json:"createdAt"`
}

func (EduAssignmentMigration) TableName() string { return "edu_assignment_migrations" }

// EduSourceAsset is the rights record of where a problem came from (DEV-026,
// FR-D01). Its flags gate student distribution, derivatives, export and cross
// organisation reuse; expiry and withdrawal take effect at access time.
type EduSourceAsset struct {
	ID              string     `json:"id" gorm:"primaryKey;size:40"`
	TenantID        string     `json:"tenantId" gorm:"size:40;not null;index"`
	WorkspaceID     string     `json:"workspaceId" gorm:"size:40;not null;index"`
	Title           string     `json:"title" gorm:"size:200;not null"`
	Kind            string     `json:"kind" gorm:"size:24;not null"` // teacher_original | textbook | exam | publisher | web | other
	Attribution     string     `json:"attribution" gorm:"size:200"`
	LicenseRef      string     `json:"licenseRef" gorm:"size:200"`
	AllowStudent    bool       `json:"allowStudent" gorm:"not null;default:false"`
	AllowDerivative bool       `json:"allowDerivative" gorm:"not null;default:false"`
	AllowExport     bool       `json:"allowExport" gorm:"not null;default:false"`
	AllowCrossOrg   bool       `json:"allowCrossOrg" gorm:"not null;default:false"`
	ExpiresAt       *time.Time `json:"expiresAt,omitempty"`
	State           string     `json:"state" gorm:"size:16;not null"` // active | withdrawn
	WithdrawnReason string     `json:"withdrawnReason,omitempty" gorm:"size:200"`
	CreatedBy       string     `json:"createdBy" gorm:"size:40;not null"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

func (EduSourceAsset) TableName() string { return "edu_source_assets" }

// EduRegrade records one re-evaluation of a main answer against a reference
// publication (DEV-025). The attempt keeps its original assignment and
// response; old and new results, the reason and the student notice are kept.
type EduRegrade struct {
	ID               string     `json:"id" gorm:"primaryKey;size:40"`
	TenantID         string     `json:"tenantId" gorm:"size:40;not null;index"`
	BatchID          string     `json:"batchId" gorm:"size:40;not null;index"`
	AssignmentID     string     `json:"assignmentId" gorm:"size:40;not null;index"`
	AttemptID        string     `json:"attemptId" gorm:"size:40;not null;uniqueIndex:ux_edu_regrade,priority:1"`
	RefPublicationID string     `json:"refPublicationId" gorm:"size:40;not null;uniqueIndex:ux_edu_regrade,priority:2"`
	StudentUserID    string     `json:"studentUserId" gorm:"size:40;not null;index"`
	OldResult        string     `json:"oldResult" gorm:"size:24;not null"`
	NewResult        string     `json:"newResult" gorm:"size:24;not null"`
	Grader           string     `json:"grader,omitempty" gorm:"size:40"`
	Reason           string     `json:"reason" gorm:"size:200;not null"`
	CreatedBy        string     `json:"createdBy" gorm:"size:40;not null"`
	CreatedAt        time.Time  `json:"createdAt"`
	NoticeSeenAt     *time.Time `json:"noticeSeenAt,omitempty"`
}

func (EduRegrade) TableName() string { return "edu_regrades" }

// EduCourse is a workspace's course configuration (DEV-014): textbook,
// knowledge points, allowed methods, notation, language and how many revisions
// to keep. Review, consent and edit-inheritance policies live in EduOrgPolicy
// and limits in EduBudgetLimit.
type EduCourse struct {
	ID              string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID        string    `json:"tenantId" gorm:"size:40;not null;index"`
	WorkspaceID     string    `json:"workspaceId" gorm:"size:40;not null;index"`
	Name            string    `json:"name" gorm:"size:120;not null"`
	Grade           string    `json:"grade" gorm:"size:40"`
	Textbook        string    `json:"textbook" gorm:"size:120"`
	TextbookEdition string    `json:"textbookEdition" gorm:"size:60"`
	KnowledgePoints string    `json:"-" gorm:"type:text"` // JSON array
	AllowedMethods  string    `json:"-" gorm:"type:text"` // JSON array
	Notation        string    `json:"notation" gorm:"size:24;not null"`
	Language        string    `json:"language" gorm:"size:12;not null"`
	RetainRevisions int       `json:"retainRevisions"`
	Version         int       `json:"version" gorm:"not null;default:1"`
	Status          string    `json:"status" gorm:"size:16;not null;default:active"`
	CreatedBy       string    `json:"createdBy" gorm:"size:40"`
	UpdatedBy       string    `json:"updatedBy" gorm:"size:40"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

func (EduCourse) TableName() string { return "edu_courses" }

// EduCourseAlignment links a lesson revision to course knowledge points. Only a
// teacher-confirmed alignment may be shown as "教材已对齐"; a suggestion (from a
// tool or an import) stays unconfirmed, and a change to the course's textbook or
// knowledge points sends confirmed alignments back to suggested.
type EduCourseAlignment struct {
	ID               string     `json:"id" gorm:"primaryKey;size:40"`
	TenantID         string     `json:"tenantId" gorm:"size:40;not null;index"`
	CourseID         string     `json:"courseId" gorm:"size:40;not null;uniqueIndex:ux_edu_course_align,priority:1"`
	LessonRevisionID string     `json:"lessonRevisionId" gorm:"size:40;not null;uniqueIndex:ux_edu_course_align,priority:2;index"`
	KnowledgePoints  string     `json:"-" gorm:"type:text"`
	State            string     `json:"state" gorm:"size:16;not null"` // suggested | confirmed | rejected
	Note             string     `json:"note,omitempty" gorm:"size:200"`
	SuggestedBy      string     `json:"suggestedBy" gorm:"size:60"`
	ConfirmedBy      string     `json:"confirmedBy,omitempty" gorm:"size:40"`
	ConfirmedAt      *time.Time `json:"confirmedAt,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

func (EduCourseAlignment) TableName() string { return "edu_course_alignments" }

// EduRosterEntry is one row of a minimal roster import (DEV-016): a
// pseudonymous external reference and display name, plus a single-use claim
// code (stored hashed) that binds a platform user to the class. It never grants
// a workspace membership.
type EduRosterEntry struct {
	ID          string     `json:"id" gorm:"primaryKey;size:40"`
	TenantID    string     `json:"tenantId" gorm:"size:40;not null;index"`
	ClassID     string     `json:"classId" gorm:"size:40;not null;uniqueIndex:ux_edu_roster_ref,priority:1"`
	ExternalRef string     `json:"externalRef" gorm:"size:40;not null;uniqueIndex:ux_edu_roster_ref,priority:2"`
	DisplayName string     `json:"displayName" gorm:"size:80"`
	State       string     `json:"state" gorm:"size:16;not null"` // pending | claimed | revoked
	SecretHash  string     `json:"-" gorm:"size:80"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	ClaimedBy   string     `json:"claimedBy,omitempty" gorm:"size:40;index"`
	ClaimedAt   *time.Time `json:"claimedAt,omitempty"`
	ImportBatch string     `json:"importBatch" gorm:"size:40;index"`
	CreatedBy   string     `json:"createdBy" gorm:"size:40"`
	CreatedAt   time.Time  `json:"createdAt"`
}

func (EduRosterEntry) TableName() string { return "edu_roster_entries" }

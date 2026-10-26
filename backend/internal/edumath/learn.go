package edumath

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// Student learning loop (FR-S01..S10): attempts, layered hints, independent
// self-test, spaced review and progress. Every answer-bearing value is read
// from the teacher lesson on the server and only returned when the task policy
// allows it; the student projection never carries it (constraint 2).

var clientKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

const maxResponse = 400

type teacherLesson struct {
	Answer struct {
		Plain string `json:"plain"`
		Latex string `json:"latex"`
		Exact string `json:"exact"`
	} `json:"answer"`
	Beats []struct {
		ID    string   `json:"id"`
		Title string   `json:"title"`
		Board []string `json:"board"`
		Say   string   `json:"say"`
	} `json:"beats"`
	Hints []struct {
		Level         int    `json:"level"`
		Kind          string `json:"kind"`
		Text          string `json:"text"`
		RevealsAnswer bool   `json:"reveals_answer"`
		Gated         bool   `json:"gated"`
	} `json:"hints"`
	Figure struct {
		Explore *struct {
			Mode       string `json:"mode"`
			Prediction struct {
				ID      string   `json:"id"`
				Prompt  string   `json:"prompt"`
				Options []string `json:"options"`
			} `json:"prediction"`
		} `json:"explore"`
	} `json:"figure"`
	CheckQuestion  map[string]any `json:"check_question"`
	CheckAnswerKey struct {
		Plain string `json:"plain"`
		Latex string `json:"latex"`
		Exact string `json:"exact"`
	} `json:"check_answer_key"`
}

func (h *API) registerLearning(g *gin.RouterGroup) {
	g.GET("/student/assignments/:id/learning", h.learningState)
	g.POST("/student/assignments/:id/attempts", h.submitAttempt)
	g.POST("/student/assignments/:id/hints", h.unlockHint)
	g.POST("/student/assignments/:id/selftest/start", h.startSelfTest)
	g.GET("/student/review", h.reviewList)
	g.GET("/student/progress", h.progress)
	g.GET("/assignments/:id/attempts", h.teacherAttempts)
	g.POST("/attempts/:id/judge", h.teacherJudge)
	g.POST("/assignments/:id/release-answer", h.releaseAnswer)
}

// learner resolves the student, the assignment audience and the start window.
func (h *API) learner(c *gin.Context) (Actor, *gorm.DB, model.EduAssignment, model.EduPublication, bool) {
	a, tx, ok := h.student(c)
	if !ok {
		return a, tx, model.EduAssignment{}, model.EduPublication{}, false
	}
	as, pub, err := studentCanAccessAssignment(tx, a.TenantID, a.UserID, c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return a, tx, as, pub, false
	}
	if as.StartsAt != nil && h.Svc.now().Before(*as.StartsAt) {
		h.fail(c, appErr(http.StatusForbidden, "NOT_STARTED", "the assignment has not started", "wait_for_start"))
		return a, tx, as, pub, false
	}
	return a, tx, as, pub, true
}

func (h *API) lessonFor(c *gin.Context, tx *gorm.DB, tenantID string, pub model.EduPublication) (teacherLesson, error) {
	var t teacherLesson
	var l model.EduLessonRevision
	var ta model.EduArtifact
	if tx.First(&l, "id = ? AND tenant_id = ?", pub.LessonRevisionID, tenantID).Error != nil ||
		tx.First(&ta, "id = ? AND tenant_id = ?", l.LessonArtifact, tenantID).Error != nil {
		return t, ErrNotFound
	}
	// The publication is bound to an exact lesson hash; refuse anything else.
	if pub.LessonHash != "" && pub.LessonHash != l.LessonHash {
		return t, appErr(http.StatusConflict, "REVISION_CONFLICT", "publication hash does not match the lesson revision", "contact_teacher")
	}
	ctx, cancel := ctx15(c)
	defer cancel()
	raw, _, err := h.Svc.Client.Artifact(ctx, tenantID, ta.ServiceArtifactID, ta.SHA256)
	if err != nil {
		return t, err
	}
	if json.Unmarshal(raw, &t) != nil || t.Answer.Exact == "" {
		return t, ErrContract
	}
	return t, nil
}

// closedForWrites uses the student's effective due time (assignment due or a later per student extension, AC-21).
func (h *API) closedForWrites(tx *gorm.DB, as model.EduAssignment, userID string) (late bool, closed bool) {
	due := h.effectiveDue(tx, as, userID)
	if due == nil || !h.Svc.now().After(*due) {
		return false, false
	}
	return true, !as.AllowLate
}

func hasValidMain(tx *gorm.DB, tenantID, asID, userID string) bool {
	var n int64
	tx.Model(&model.EduAttempt{}).Where("tenant_id = ? AND assignment_id = ? AND student_user_id = ? AND item = ? AND valid = ?", tenantID, asID, userID, "main", true).Count(&n)
	return n > 0
}

func (h *API) answerReleased(tx *gorm.DB, as model.EduAssignment, userID string) bool {
	if as.AnswerReleasedAt != nil {
		return true
	}
	switch as.AnswerPolicy {
	case "after_due":
		// Pending user decision (docs 05 question 10): release follows the student's own effective due time.
		due := h.effectiveDue(tx, as, userID)
		return due != nil && !h.Svc.now().Before(*due)
	case "after_submission":
		return hasValidMain(tx, as.TenantID, as.ID, userID)
	}
	return false
}

func unlockedLevels(tx *gorm.DB, tenantID, asID, userID string) []int {
	var lv []int
	tx.Model(&model.EduHintUnlock{}).Where("tenant_id = ? AND assignment_id = ? AND student_user_id = ?", tenantID, asID, userID).Order("level").Pluck("level", &lv)
	return lv
}

func maxLevel(lv []int) int {
	m := 0
	for _, x := range lv {
		if x > m {
			m = x
		}
	}
	return m
}

func normResponse(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// submitAttempt records one response (idempotent on clientKey). Blank answers are
// recorded as invalid; the first valid main attempt is frozen; the grader runs in
// the isolated math service and its failure is recorded as undetermined, never
// as wrong.
func (h *API) submitAttempt(c *gin.Context) {
	a, tx, as, pub, ok := h.learner(c)
	if !ok {
		return
	}
	var in struct {
		Item         string `json:"item"`
		Kind         string `json:"kind"`
		Response     string `json:"response"`
		ClientKey    string `json:"clientKey"`
		ReviewItemID string `json:"reviewItemId"`
	}
	if c.ShouldBindJSON(&in) != nil || !clientKeyRe.MatchString(in.ClientKey) || len(in.Response) > maxResponse {
		reply(c, 400, nil, "item, kind and an 8..64 char clientKey are required; response <= 400 bytes")
		return
	}
	var prev model.EduAttempt
	if tx.First(&prev, "tenant_id = ? AND student_user_id = ? AND client_key = ?", a.TenantID, a.UserID, in.ClientKey).Error == nil {
		if prev.AssignmentID != as.ID {
			h.fail(c, appErr(409, "IDEMPOTENCY_CONFLICT", "clientKey already used for another assignment", "new_client_key"))
			return
		}
		reply(c, 200, gin.H{"attempt": prev, "replayed": true}, "ok")
		return
	}
	if in.Item == "" {
		in.Item = "main"
	}
	// With an approved variant set attached, the set is the self-test unit and its item ids replace "check".
	var setItems []gin.H
	var setKeys map[string]answerKey
	if as.VariantSetID != "" && in.Item != "main" {
		var err error
		if setItems, setKeys, err = studentVariantItems(tx, a.TenantID, as.VariantSetID); err != nil {
			h.fail(c, err)
			return
		}
	}
	_, isSetItem := setKeys[in.Item]
	itemOK := in.Item == "main" || (as.VariantSetID == "" && in.Item == "check") || isSetItem
	if !itemOK || (in.Kind != "answer" && in.Kind != "no_idea") || (in.Kind == "no_idea" && in.Item != "main") {
		reply(c, 400, nil, "item must be main, check or a self-test set item; kind answer|no_idea (no_idea only for main)")
		return
	}
	late, closed := h.closedForWrites(tx, as, a.UserID)
	if closed {
		h.fail(c, appErr(409, "ASSIGNMENT_CLOSED", "the due time has passed and late submission is off", "contact_teacher"))
		return
	}
	var st model.EduSelfTest
	if in.Item != "main" {
		if tx.First(&st, "tenant_id = ? AND assignment_id = ? AND student_user_id = ?", a.TenantID, as.ID, a.UserID).Error != nil || st.State != "started" {
			h.fail(c, appErr(409, "SELFTEST_NOT_STARTED", "start the self-test before answering it; it can be submitted once", "start_selftest"))
			return
		}
	}
	if isSetItem {
		var n int64
		tx.Model(&model.EduAttempt{}).Where("tenant_id = ? AND assignment_id = ? AND student_user_id = ? AND item = ? AND valid = ?", a.TenantID, as.ID, a.UserID, in.Item, true).Count(&n)
		if n > 0 {
			h.fail(c, appErr(409, "ITEM_ALREADY_SUBMITTED", "each self-test item is submitted once", "next_item"))
			return
		}
	}
	var rv model.EduReviewItem
	if in.ReviewItemID != "" {
		if in.Item != "main" || tx.First(&rv, "id = ? AND tenant_id = ? AND assignment_id = ? AND student_user_id = ? AND state = ?", in.ReviewItemID, a.TenantID, as.ID, a.UserID, "pending").Error != nil {
			h.fail(c, ErrNotFound)
			return
		}
		if h.Svc.now().Before(rv.DueAt) {
			h.fail(c, appErr(409, "REVIEW_NOT_DUE", "this review is not due yet", "wait"))
			return
		}
	}
	lesson, err := h.lessonFor(c, tx, a.TenantID, pub)
	if err != nil {
		h.fail(c, err)
		return
	}
	lv := unlockedLevels(tx, a.TenantID, as.ID, a.UserID)
	at := model.EduAttempt{ID: flow.NewID("eat_"), TenantID: a.TenantID, AssignmentID: as.ID, StudentUserID: a.UserID, ClientKey: in.ClientKey,
		Item: in.Item, Kind: in.Kind, Response: strings.TrimSpace(in.Response), HintLevel: maxLevel(lv), Late: late, ReviewItemID: rv.ID, CreatedAt: h.Svc.now()}
	switch {
	case in.Kind == "no_idea":
		at.Valid, at.Result, at.Response = true, "no_idea", ""
	case at.Response == "":
		at.Valid, at.InvalidReason, at.Result = false, "empty", "invalid"
	default:
		var last model.EduAttempt
		if tx.Where("tenant_id = ? AND assignment_id = ? AND student_user_id = ? AND item = ? AND valid = ? AND kind = ?", a.TenantID, as.ID, a.UserID, in.Item, true, "answer").
			Order("created_at desc").First(&last).Error == nil && in.ReviewItemID == "" && normResponse(last.Response) == normResponse(at.Response) {
			at.Valid, at.InvalidReason, at.Result = false, "duplicate", "invalid"
			break
		}
		exp := map[string]any{"exact": lesson.Answer.Exact, "plain": lesson.Answer.Plain}
		if in.Item == "check" {
			exp = map[string]any{"exact": lesson.CheckAnswerKey.Exact, "plain": lesson.CheckAnswerKey.Plain}
		}
		if isSetItem {
			exp = map[string]any{"exact": setKeys[in.Item].Exact, "plain": setKeys[in.Item].Plain}
		}
		ctx, cancel := ctx15(c)
		g, gerr := h.Svc.Client.Grade(ctx, a.TenantID, map[string]any{"expected": exp, "response": at.Response,
			"approx": map[string]any{"allowed": as.ApproxAllowed, "rel_tol": 1e-3}, "unit_policy": as.UnitPolicy})
		cancel()
		switch {
		case gerr != nil:
			at.Valid, at.Result, at.Reason = true, "undetermined", "grader_unavailable"
		case g.Result == "invalid":
			at.Valid, at.InvalidReason, at.Result, at.Grader = false, g.Reason, "invalid", g.Grader
		default:
			at.Valid, at.Result, at.Reason, at.Grader = true, g.Result, g.Reason, g.Grader
		}
	}
	if at.Valid && in.Item == "main" && !hasValidMain(tx, a.TenantID, as.ID, a.UserID) {
		at.FirstValid = true
	}
	if in.Item == "main" {
		at.Independent = at.HintLevel == 0
	} else {
		at.Independent = !st.HelpUsed
	}
	err = tx.Transaction(func(t *gorm.DB) error {
		if err := t.Create(&at).Error; err != nil {
			return err
		}
		if in.Item == "check" && at.Valid {
			now := h.Svc.now()
			if err := t.Model(&st).Updates(map[string]any{"state": "submitted", "submitted_at": now, "attempt_id": at.ID}).Error; err != nil {
				return err
			}
		}
		if isSetItem && at.Valid && setDone(t, a.TenantID, as.ID, a.UserID, setItems) {
			now := h.Svc.now()
			if err := t.Model(&st).Updates(map[string]any{"state": "submitted", "submitted_at": now, "attempt_id": at.ID}).Error; err != nil {
				return err
			}
			st.State = "submitted"
		}
		if in.Item == "main" && at.Valid {
			return h.scheduleReview(t, a, as, at, rv)
		}
		return nil
	})
	if err != nil {
		var again model.EduAttempt
		if tx.First(&again, "tenant_id = ? AND student_user_id = ? AND client_key = ?", a.TenantID, a.UserID, in.ClientKey).Error == nil {
			reply(c, 200, gin.H{"attempt": again, "replayed": true}, "ok")
			return
		}
		h.fail(c, err)
		return
	}
	h.Svc.audit(tx, a, "attempt.submit", as.ID, at.ID+" item="+at.Item+" result="+at.Result)
	if isSetItem && st.State != "submitted" {
		// FR-S08 / AC-28: no correctness feedback until every item of the unit is submitted.
		reply(c, 201, gin.H{"attempt": maskSetAttempt(at), "answerAvailable": false, "selfTestComplete": false}, "ok")
		return
	}
	reply(c, 201, gin.H{"attempt": at, "answerAvailable": h.answerReleased(tx, as, a.UserID), "selfTestComplete": in.Item != "main" && st.State == "submitted"}, "ok")
}

var reviewGaps = []time.Duration{24 * time.Hour, 72 * time.Hour, 168 * time.Hour}

func needsReview(at model.EduAttempt) string {
	switch {
	case at.Kind == "no_idea":
		return "no_idea"
	case at.Result == "incorrect" || at.Result == "needs_exact":
		return "incorrect"
	case at.HintLevel >= 4:
		return "heavy_hints"
	}
	return ""
}

// scheduleReview: +1d after a miss; a due review answered correctly moves to +3d, then +7d, then done.
func (h *API) scheduleReview(t *gorm.DB, a Actor, as model.EduAssignment, at model.EduAttempt, rv model.EduReviewItem) error {
	now := h.Svc.now()
	good := at.Result == "correct" || at.Result == "correct_approx"
	if rv.ID != "" {
		up := map[string]any{"updated_at": now}
		switch {
		case good && rv.Stage >= len(reviewGaps):
			up["state"] = "done"
		case good:
			up["stage"], up["due_at"] = rv.Stage+1, now.Add(reviewGaps[rv.Stage])
		case at.Result == "incorrect" || at.Result == "no_idea" || at.Result == "needs_exact":
			up["stage"], up["due_at"] = 1, now.Add(reviewGaps[0])
		default:
			return nil // undetermined / needs_teacher: keep the item due
		}
		return t.Model(&rv).Updates(up).Error
	}
	why := needsReview(at)
	if why == "" {
		return nil
	}
	var cur model.EduReviewItem
	if t.First(&cur, "tenant_id = ? AND assignment_id = ? AND student_user_id = ?", a.TenantID, as.ID, a.UserID).Error == nil {
		if cur.State == "pending" {
			return nil
		}
		return t.Model(&cur).Updates(map[string]any{"state": "pending", "stage": 1, "reason": why, "due_at": now.Add(reviewGaps[0]), "updated_at": now}).Error
	}
	return t.Create(&model.EduReviewItem{ID: flow.NewID("erv_"), TenantID: a.TenantID, AssignmentID: as.ID, StudentUserID: a.UserID,
		Reason: why, Stage: 1, DueAt: now.Add(reviewGaps[0]), State: "pending", CreatedAt: now, UpdatedAt: now}).Error
}

// unlockHint returns exactly one level. Levels open in order; level 4 needs an
// attempt (or "no idea"); level 5 (full solution) follows the answer policy.
func (h *API) unlockHint(c *gin.Context) {
	a, tx, as, pub, ok := h.learner(c)
	if !ok {
		return
	}
	var in struct {
		Level int `json:"level"`
	}
	if c.ShouldBindJSON(&in) != nil || in.Level < 1 || in.Level > 5 {
		reply(c, 400, nil, "level must be 1..5")
		return
	}
	lv := unlockedLevels(tx, a.TenantID, as.ID, a.UserID)
	already := false
	for _, x := range lv {
		already = already || x == in.Level
	}
	if !already {
		if in.Level != maxLevel(lv)+1 {
			h.fail(c, appErr(409, "HINT_ORDER", "hints open one level at a time", "open_previous_level"))
			return
		}
		if in.Level == 4 && !hasValidMain(tx, a.TenantID, as.ID, a.UserID) {
			h.fail(c, appErr(409, "HINT_GATED", "submit an attempt or choose 'no idea' first", "attempt_first"))
			return
		}
		if in.Level == 5 && !h.answerReleased(tx, as, a.UserID) {
			h.fail(c, appErr(409, "ANSWER_NOT_RELEASED", "the full solution is not open under this assignment's policy", "wait_for_release"))
			return
		}
	}
	lesson, err := h.lessonFor(c, tx, a.TenantID, pub)
	if err != nil {
		h.fail(c, err)
		return
	}
	var body gin.H
	if in.Level == 5 {
		beats := []gin.H{}
		for _, b := range lesson.Beats {
			beats = append(beats, gin.H{"id": b.ID, "title": b.Title, "board": b.Board, "say": b.Say})
		}
		body = gin.H{"level": 5, "kind": "full_solution", "beats": beats, "answer": gin.H{"plain": lesson.Answer.Plain, "latex": lesson.Answer.Latex}}
	} else {
		for _, x := range lesson.Hints {
			if x.Level == in.Level {
				if x.RevealsAnswer || hintLeaks(x.Text, lesson) {
					reply(c, 500, nil, "hint rejected by answer guard")
					return
				}
				body = gin.H{"level": x.Level, "kind": x.Kind, "text": x.Text}
			}
		}
		if body == nil {
			h.fail(c, ErrNotFound)
			return
		}
	}
	if !already {
		// A savepoint keeps a concurrent duplicate (unique index) from aborting the request transaction.
		if tx.Transaction(func(t *gorm.DB) error {
			return t.Create(&model.EduHintUnlock{ID: flow.NewID("ehu_"), TenantID: a.TenantID, AssignmentID: as.ID, StudentUserID: a.UserID, Level: in.Level, CreatedAt: h.Svc.now()}).Error
		}) == nil {
			h.Svc.audit(tx, a, "hint.unlock", as.ID, "level="+strconv.Itoa(in.Level))
		}
		// Asking for help during the self-test ends its independence (FR-S08).
		tx.Model(&model.EduSelfTest{}).Where("tenant_id = ? AND assignment_id = ? AND student_user_id = ? AND state = ?", a.TenantID, as.ID, a.UserID, "started").Update("help_used", true)
	}
	reply(c, 200, body, "ok")
}

// hintLeaks: a hint below level 5 must not contain the answer or the self-test key.
func hintLeaks(text string, l teacherLesson) bool {
	t := normResponse(text)
	for _, m := range []string{l.Answer.Plain, l.Answer.Exact, l.Answer.Latex, l.CheckAnswerKey.Plain, l.CheckAnswerKey.Exact} {
		m = normResponse(m)
		if len(m) >= 3 && strings.Contains(t, m) {
			return true
		}
	}
	return false
}

func (h *API) startSelfTest(c *gin.Context) {
	a, tx, as, pub, ok := h.learner(c)
	if !ok {
		return
	}
	if _, closed := h.closedForWrites(tx, as, a.UserID); closed {
		h.fail(c, appErr(409, "ASSIGNMENT_CLOSED", "the due time has passed and late submission is off", "contact_teacher"))
		return
	}
	lesson, err := h.lessonFor(c, tx, a.TenantID, pub)
	if err != nil {
		h.fail(c, err)
		return
	}
	var st model.EduSelfTest
	if tx.First(&st, "tenant_id = ? AND assignment_id = ? AND student_user_id = ?", a.TenantID, as.ID, a.UserID).Error != nil {
		saw := false
		for _, x := range unlockedLevels(tx, a.TenantID, as.ID, a.UserID) {
			saw = saw || x == 5
		}
		st = model.EduSelfTest{ID: flow.NewID("est_"), TenantID: a.TenantID, AssignmentID: as.ID, StudentUserID: a.UserID, State: "started", SawSolution: saw, StartedAt: h.Svc.now()}
		if err := tx.Transaction(func(t *gorm.DB) error { return t.Create(&st).Error }); err != nil {
			tx.First(&st, "tenant_id = ? AND assignment_id = ? AND student_user_id = ?", a.TenantID, as.ID, a.UserID)
		}
	}
	if as.VariantSetID != "" {
		items, _, err := studentVariantItems(tx, a.TenantID, as.VariantSetID)
		if err != nil {
			h.fail(c, err)
			return
		}
		reply(c, 200, gin.H{"selfTest": st, "measurement": "set", "questions": items}, "ok")
		return
	}
	q := gin.H{}
	for _, k := range []string{"id", "prompt", "params", "answer_format", "unit"} {
		if v, ok := lesson.CheckQuestion[k]; ok {
			q[k] = v
		}
	}
	reply(c, 200, gin.H{"selfTest": st, "question": q}, "ok")
}

func (h *API) learningState(c *gin.Context) {
	a, tx, as, pub, ok := h.learner(c)
	if !ok {
		return
	}
	var ats []model.EduAttempt
	tx.Where("tenant_id = ? AND assignment_id = ? AND student_user_id = ?", a.TenantID, as.ID, a.UserID).Order("created_at").Find(&ats)
	var st model.EduSelfTest
	hasST := tx.First(&st, "tenant_id = ? AND assignment_id = ? AND student_user_id = ?", a.TenantID, as.ID, a.UserID).Error == nil
	var rv model.EduReviewItem
	hasRV := tx.First(&rv, "tenant_id = ? AND assignment_id = ? AND student_user_id = ?", a.TenantID, as.ID, a.UserID).Error == nil
	late, closed := h.closedForWrites(tx, as, a.UserID)
	out := gin.H{
		"policy": gin.H{"startsAt": as.StartsAt, "dueAt": as.DueAt, "allowLate": as.AllowLate, "answerPolicy": as.AnswerPolicy,
			"answerAvailable": h.answerReleased(tx, as, a.UserID), "late": late, "closed": closed, "unitPolicy": as.UnitPolicy, "approxAllowed": as.ApproxAllowed},
		"attempts": ats, "hintLevels": unlockedLevels(tx, a.TenantID, as.ID, a.UserID), "selfTest": nil, "review": nil,
	}
	if as.VariantSetID != "" && (!hasST || st.State != "submitted") {
		for i := range ats {
			if ats[i].Item != "main" {
				ats[i] = maskSetAttempt(ats[i])
			}
		}
	}
	if due := h.effectiveDue(tx, as, a.UserID); due != nil && (as.DueAt == nil || !due.Equal(*as.DueAt)) {
		out["policy"].(gin.H)["extendedDueAt"] = due
	}
	if hasST && as.VariantSetID != "" {
		stv := gin.H{"state": st.State, "helpUsed": st.HelpUsed, "sawSolution": st.SawSolution, "startedAt": st.StartedAt, "submittedAt": st.SubmittedAt, "measurement": "set"}
		if items, keys, err := studentVariantItems(tx, a.TenantID, as.VariantSetID); err == nil {
			stv["questions"] = items
			if st.State == "submitted" {
				ks := gin.H{}
				for id, k := range keys {
					ks[id] = gin.H{"plain": k.Plain, "latex": k.Latex}
				}
				stv["answerKeys"] = ks
			}
		}
		out["selfTest"] = stv
	} else if hasST {
		stv := gin.H{"state": st.State, "helpUsed": st.HelpUsed, "sawSolution": st.SawSolution, "startedAt": st.StartedAt, "submittedAt": st.SubmittedAt}
		if st.State == "submitted" { // the key is shown only after submission
			if lesson, err := h.lessonFor(c, tx, a.TenantID, pub); err == nil {
				stv["answerKey"] = gin.H{"plain": lesson.CheckAnswerKey.Plain, "latex": lesson.CheckAnswerKey.Latex}
			}
		}
		out["selfTest"] = stv
	}
	if hasRV {
		out["review"] = rv
	}
	var preds []model.EduPrediction
	tx.Where("tenant_id = ? AND assignment_id = ? AND student_user_id = ?", a.TenantID, as.ID, a.UserID).Order("created_at").Find(&preds)
	out["predictions"] = preds
	reply(c, 200, out, "ok")
}

func (h *API) reviewList(c *gin.Context) {
	a, tx, ok := h.student(c)
	if !ok {
		return
	}
	if !consentOK(tx, a.TenantID, a.UserID) {
		h.fail(c, ErrConsent)
		return
	}
	var items []model.EduReviewItem
	tx.Where("tenant_id = ? AND student_user_id = ? AND state = ?", a.TenantID, a.UserID, "pending").Order("due_at").Find(&items)
	now := h.Svc.now()
	out := []gin.H{}
	for _, it := range items {
		as, _, err := studentCanAccessAssignment(tx, a.TenantID, a.UserID, it.AssignmentID)
		if err != nil {
			continue
		}
		out = append(out, gin.H{"id": it.ID, "assignmentId": it.AssignmentID, "title": as.Title, "reason": it.Reason, "stage": it.Stage, "dueAt": it.DueAt, "due": !now.Before(it.DueAt)})
	}
	reply(c, 200, out, "ok")
}

func effective(at model.EduAttempt) string {
	if at.TeacherResult != "" {
		return at.TeacherResult
	}
	return at.Result
}

func statusLabel(r string) string {
	switch r {
	case "":
		return "untested"
	case "correct", "correct_approx":
		return "correct"
	case "needs_teacher", "undetermined":
		return "pending_judgement"
	case "partial":
		return "partial"
	}
	return "incorrect"
}

// MinIndependentEvidence is how many independent self-test results are needed
// before progress is reported as more than "insufficient evidence" (FR-S10).
const MinIndependentEvidence = 3

func (h *API) progress(c *gin.Context) {
	a, tx, ok := h.student(c)
	if !ok {
		return
	}
	if !consentOK(tx, a.TenantID, a.UserID) {
		h.fail(c, ErrConsent)
		return
	}
	var ats []model.EduAttempt
	tx.Where("tenant_id = ? AND student_user_id = ? AND valid = ?", a.TenantID, a.UserID, true).Order("created_at").Find(&ats)
	type row struct {
		AssignmentID, Title, First, Check string
		CheckIndependent                  bool
		Attempts                          int
		IsSet                             bool
		Checks                            []model.EduAttempt
	}
	rows := map[string]*row{}
	order := []string{}
	indep, indepCorrect := 0, 0
	for _, at := range ats {
		r := rows[at.AssignmentID]
		if r == nil {
			as, _, err := studentCanAccessAssignment(tx, a.TenantID, a.UserID, at.AssignmentID)
			if err != nil {
				continue
			}
			r = &row{AssignmentID: as.ID, Title: as.Title, IsSet: as.VariantSetID != ""}
			rows[at.AssignmentID] = r
			order = append(order, at.AssignmentID)
		}
		if at.Item == "main" {
			r.Attempts++
			if at.FirstValid {
				r.First = statusLabel(effective(at))
			}
		} else {
			r.Checks = append(r.Checks, at)
		}
	}
	// The self-test unit (one check question or a whole variant set) is one measurement (FR-S08).
	for _, id := range order {
		r := rows[id]
		if len(r.Checks) == 0 {
			continue
		}
		if r.IsSet {
			var st model.EduSelfTest
			if tx.First(&st, "tenant_id = ? AND assignment_id = ? AND student_user_id = ?", a.TenantID, id, a.UserID).Error != nil || st.State != "submitted" {
				r.Check = "in_progress" // no correctness before the whole unit is submitted
				continue
			}
		}
		r.Check, r.CheckIndependent = unitLabel(r.Checks), unitIndependent(r.Checks)
		if r.CheckIndependent && r.Check != "pending_judgement" {
			indep++
			if r.Check == "correct" {
				indepCorrect++
			}
		}
	}
	list := []gin.H{}
	for _, id := range order {
		r := rows[id]
		list = append(list, gin.H{"assignmentId": r.AssignmentID, "title": r.Title, "firstAttempt": r.First, "attempts": r.Attempts,
			"selfTest": r.Check, "selfTestIndependent": r.CheckIndependent,
			// The self-test is an isomorphic variant (changed numbers): near transfer, never reported as far transfer (AC-29).
			"selfTestKind": "isomorphic_variant"})
	}
	sum := gin.H{"independentResults": indep, "minimumNeeded": MinIndependentEvidence, "evidence": "insufficient"}
	if indep >= MinIndependentEvidence {
		sum["evidence"] = "sufficient"
		sum["independentCorrect"] = indepCorrect
	}
	reply(c, 200, gin.H{"summary": sum, "assignments": list}, "ok")
}

func (h *API) staffAssignment(c *gin.Context) (Actor, *gorm.DB, model.EduAssignment, bool) {
	a, tx, ok := h.staff(c)
	if !ok {
		return a, tx, model.EduAssignment{}, false
	}
	var as model.EduAssignment
	if tx.First(&as, "id = ? AND tenant_id = ?", c.Param("id"), a.TenantID).Error != nil {
		h.fail(c, ErrNotFound)
		return a, tx, as, false
	}
	if _, err := classForStaff(tx, a, as.ClassID); err != nil {
		h.fail(c, err)
		return a, tx, as, false
	}
	return a, tx, as, true
}

func (h *API) teacherAttempts(c *gin.Context) {
	a, tx, as, ok := h.staffAssignment(c)
	if !ok {
		return
	}
	var ats []model.EduAttempt
	tx.Where("tenant_id = ? AND assignment_id = ?", a.TenantID, as.ID).Order("created_at").Find(&ats)
	var hs []model.EduHintUnlock
	tx.Where("tenant_id = ? AND assignment_id = ?", a.TenantID, as.ID).Order("created_at").Find(&hs)
	var sts []model.EduSelfTest
	tx.Where("tenant_id = ? AND assignment_id = ?", a.TenantID, as.ID).Find(&sts)
	reply(c, 200, gin.H{"assignment": as, "attempts": ats, "hints": hs, "selfTests": sts}, "ok")
}

// teacherJudge records the teacher's judgement next to the automatic one; it never overwrites it.
func (h *API) teacherJudge(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var in struct {
		Result string `json:"result"`
		Note   string `json:"note"`
	}
	if c.ShouldBindJSON(&in) != nil || !map[string]bool{"correct": true, "incorrect": true, "partial": true}[in.Result] || len(in.Note) > 500 {
		reply(c, 400, nil, "result must be correct|incorrect|partial; note <= 500 bytes")
		return
	}
	var at model.EduAttempt
	var as model.EduAssignment
	if tx.First(&at, "id = ? AND tenant_id = ?", c.Param("id"), a.TenantID).Error != nil || tx.First(&as, "id = ? AND tenant_id = ?", at.AssignmentID, a.TenantID).Error != nil {
		h.fail(c, ErrNotFound)
		return
	}
	if _, err := classForStaff(tx, a, as.ClassID); err != nil {
		h.fail(c, err)
		return
	}
	if !at.Valid {
		h.fail(c, appErr(409, "INVALID_ATTEMPT", "invalid attempts are not judged", ""))
		return
	}
	tx.Model(&at).Updates(map[string]any{"teacher_result": in.Result, "teacher_note": strings.TrimSpace(in.Note), "teacher_by": a.UserID})
	h.Svc.audit(tx, a, "attempt.judge", as.ID, at.ID+" auto="+at.Result+" teacher="+in.Result)
	reply(c, 200, gin.H{"id": at.ID, "result": at.Result, "teacherResult": in.Result}, "ok")
}

func (h *API) releaseAnswer(c *gin.Context) {
	a, tx, as, ok := h.staffAssignment(c)
	if !ok {
		return
	}
	if as.AnswerReleasedAt == nil {
		now := h.Svc.now()
		tx.Model(&as).Update("answer_released_at", now)
		as.AnswerReleasedAt = &now
		h.Svc.audit(tx, a, "assignment.release_answer", as.ClassID, as.ID)
	}
	reply(c, 200, gin.H{"id": as.ID, "answerReleasedAt": as.AnswerReleasedAt}, "ok")
}

func setDone(tx *gorm.DB, tenantID, asID, userID string, items []gin.H) bool {
	var done []string
	tx.Model(&model.EduAttempt{}).Where("tenant_id = ? AND assignment_id = ? AND student_user_id = ? AND item <> ? AND valid = ?", tenantID, asID, userID, "main", true).
		Distinct("item").Pluck("item", &done)
	got := map[string]bool{}
	for _, d := range done {
		got[d] = true
	}
	for _, it := range items {
		if !got[it["id"].(string)] {
			return false
		}
	}
	return true
}

// maskSetAttempt hides correctness of a set item until the whole unit is submitted (FR-S08).
func maskSetAttempt(at model.EduAttempt) model.EduAttempt {
	if at.Valid {
		at.Result, at.Reason, at.Grader = "recorded", "", ""
		at.TeacherResult, at.TeacherNote, at.TeacherBy = "", "", ""
	}
	return at
}

// unitLabel summarises a self-test unit: every item correct, some correct (partial), pending if any
// item awaits judgement, otherwise incorrect. A single check question keeps its own label.
func unitLabel(ats []model.EduAttempt) string {
	if len(ats) == 1 {
		return statusLabel(effective(ats[0]))
	}
	correct := 0
	for _, at := range ats {
		switch statusLabel(effective(at)) {
		case "pending_judgement":
			return "pending_judgement"
		case "correct":
			correct++
		}
	}
	switch {
	case correct == len(ats):
		return "correct"
	case correct > 0:
		return "partial"
	}
	return "incorrect"
}

func unitIndependent(ats []model.EduAttempt) bool {
	for _, at := range ats {
		if !at.Independent {
			return false
		}
	}
	return len(ats) > 0
}

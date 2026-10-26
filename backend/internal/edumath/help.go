package edumath

import (
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// Help requests (FR-S11), grouped teacher replies (FR-T13) and the class
// dashboard (FR-T12). Requests and raw attempts are private to the student and
// the class's teachers; a public class note is checked for personal identifiers.

var helpKinds = map[string]int{"content_error": 1, "access_problem": 1, "dont_understand": 2, "ask_teacher": 2}

var (
	beatIDRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,40}$`)
	emailRe  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	phoneRe  = regexp.MustCompile(`\d{7,}`)
)

func (h *API) registerHelp(g *gin.RouterGroup) {
	g.POST("/student/assignments/:id/help", h.requestHelp)
	g.GET("/student/help", h.studentHelp)
	g.GET("/assignments/:id/help", h.teacherHelp)
	g.POST("/assignments/:id/help/reply", h.replyHelp)
	g.GET("/assignments/:id/dashboard", h.dashboard)
}

func (h *API) requestHelp(c *gin.Context) {
	a, tx, ok := h.student(c)
	if !ok {
		return
	}
	// Reporting a problem must work even outside the start/due window, so only the audience check applies.
	as, _, err := studentCanAccessAssignment(tx, a.TenantID, a.UserID, c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	var in struct {
		Kind      string `json:"kind"`
		BeatID    string `json:"beatId"`
		Message   string `json:"message"`
		ClientKey string `json:"clientKey"`
	}
	if c.ShouldBindJSON(&in) != nil || !clientKeyRe.MatchString(in.ClientKey) || len(in.Message) > 500 {
		reply(c, 400, nil, "kind, clientKey (8..64) required; message <= 500 bytes")
		return
	}
	pr, known := helpKinds[in.Kind]
	if !known || (in.BeatID != "" && !beatIDRe.MatchString(in.BeatID)) {
		reply(c, 400, nil, "kind must be content_error|access_problem|dont_understand|ask_teacher")
		return
	}
	var prev model.EduHelpRequest
	if tx.First(&prev, "tenant_id = ? AND student_user_id = ? AND client_key = ?", a.TenantID, a.UserID, in.ClientKey).Error == nil {
		reply(c, 200, gin.H{"request": prev, "replayed": true}, "ok")
		return
	}
	now := h.Svc.now()
	r := model.EduHelpRequest{ID: flow.NewID("ehr_"), TenantID: a.TenantID, ClassID: as.ClassID, AssignmentID: as.ID, StudentUserID: a.UserID,
		ClientKey: in.ClientKey, Kind: in.Kind, BeatID: in.BeatID, Message: strings.TrimSpace(in.Message),
		GroupKey: as.ID + "|" + in.Kind + "|" + in.BeatID, Priority: pr, State: "open", CreatedAt: now, UpdatedAt: now}
	if err := tx.Transaction(func(t *gorm.DB) error { return t.Create(&r).Error }); err != nil {
		if tx.First(&prev, "tenant_id = ? AND student_user_id = ? AND client_key = ?", a.TenantID, a.UserID, in.ClientKey).Error == nil {
			reply(c, 200, gin.H{"request": prev, "replayed": true}, "ok")
			return
		}
		h.fail(c, err)
		return
	}
	h.Svc.audit(tx, a, "help.request", as.ID, r.ID+" kind="+r.Kind)
	reply(c, 201, gin.H{"request": r}, "ok")
}

// studentHelp: the student's own requests with their replies, plus public notes
// for assignments the student can currently access. Never other students' requests.
func (h *API) studentHelp(c *gin.Context) {
	a, tx, ok := h.student(c)
	if !ok {
		return
	}
	if !consentOK(tx, a.TenantID, a.UserID) {
		h.fail(c, ErrConsent)
		return
	}
	var rs []model.EduHelpRequest
	tx.Where("tenant_id = ? AND student_user_id = ?", a.TenantID, a.UserID).Order("created_at desc").Find(&rs)
	own := []gin.H{}
	for _, r := range rs {
		row := gin.H{"id": r.ID, "assignmentId": r.AssignmentID, "kind": r.Kind, "beatId": r.BeatID, "message": r.Message, "state": r.State, "createdAt": r.CreatedAt}
		var rep model.EduHelpReply
		if r.ReplyID != "" && tx.First(&rep, "id = ? AND tenant_id = ?", r.ReplyID, a.TenantID).Error == nil {
			row["reply"] = gin.H{"body": rep.Body, "public": rep.Public, "createdAt": rep.CreatedAt}
		}
		own = append(own, row)
	}
	var classIDs []string
	tx.Model(&model.EduEnrollment{}).Where("tenant_id = ? AND student_user_id = ? AND status = ?", a.TenantID, a.UserID, "active").Pluck("class_id", &classIDs)
	notes := []gin.H{}
	if len(classIDs) > 0 {
		var reps []model.EduHelpReply
		tx.Where("tenant_id = ? AND class_id IN ? AND public = ?", a.TenantID, classIDs, true).Order("created_at desc").Limit(200).Find(&reps)
		for _, rep := range reps {
			if _, _, err := studentCanAccessAssignment(tx, a.TenantID, a.UserID, rep.AssignmentID); err == nil {
				notes = append(notes, gin.H{"assignmentId": rep.AssignmentID, "body": rep.Body, "createdAt": rep.CreatedAt})
			}
		}
	}
	reply(c, 200, gin.H{"requests": own, "classNotes": notes}, "ok")
}

func (h *API) teacherHelp(c *gin.Context) {
	a, tx, as, ok := h.staffAssignment(c)
	if !ok {
		return
	}
	var rs []model.EduHelpRequest
	tx.Where("tenant_id = ? AND assignment_id = ?", a.TenantID, as.ID).Order("created_at").Find(&rs)
	type group struct {
		GroupKey string                 `json:"groupKey"`
		Kind     string                 `json:"kind"`
		BeatID   string                 `json:"beatId"`
		Priority int                    `json:"priority"`
		Open     int                    `json:"open"`
		Requests []model.EduHelpRequest `json:"requests"`
	}
	idx := map[string]*group{}
	list := []*group{}
	for _, r := range rs {
		g := idx[r.GroupKey]
		if g == nil {
			g = &group{GroupKey: r.GroupKey, Kind: r.Kind, BeatID: r.BeatID, Priority: r.Priority}
			idx[r.GroupKey] = g
			list = append(list, g)
		}
		g.Requests = append(g.Requests, r)
		if r.State == "open" {
			g.Open++
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Priority != list[j].Priority {
			return list[i].Priority < list[j].Priority
		}
		return list[i].Open > list[j].Open
	})
	reply(c, 200, list, "ok")
}

// containsPersonalInfo flags a public note that names an enrolled student or carries an email/phone number.
func containsPersonalInfo(tx *gorm.DB, tenantID, classID, body string) bool {
	if emailRe.MatchString(body) || phoneRe.MatchString(body) {
		return true
	}
	var ids []string
	tx.Model(&model.EduEnrollment{}).Where("tenant_id = ? AND class_id = ?", tenantID, classID).Pluck("student_user_id", &ids)
	if len(ids) == 0 {
		return false
	}
	var users []model.User
	tx.Where("id IN ?", ids).Find(&users)
	low := strings.ToLower(body)
	for _, u := range users {
		for _, n := range []string{u.ID, u.Username, u.DisplayName} {
			if len([]rune(n)) >= 2 && strings.Contains(low, strings.ToLower(n)) {
				return true
			}
		}
	}
	return false
}

func (h *API) replyHelp(c *gin.Context) {
	a, tx, as, ok := h.staffAssignment(c)
	if !ok {
		return
	}
	var in struct {
		GroupKey string `json:"groupKey"`
		Body     string `json:"body"`
		Public   bool   `json:"public"`
	}
	if c.ShouldBindJSON(&in) != nil || strings.TrimSpace(in.Body) == "" || len(in.Body) > 2000 || !strings.HasPrefix(in.GroupKey, as.ID+"|") {
		reply(c, 400, nil, "groupKey of this assignment and a body (<= 2000 bytes) are required")
		return
	}
	if in.Public && containsPersonalInfo(tx, a.TenantID, as.ClassID, in.Body) {
		h.fail(c, appErr(http.StatusUnprocessableEntity, "PERSONAL_INFO", "a public note must not name students or contain contact details", "remove_personal_info"))
		return
	}
	now := h.Svc.now()
	rep := model.EduHelpReply{ID: flow.NewID("ehp_"), TenantID: a.TenantID, ClassID: as.ClassID, AssignmentID: as.ID, GroupKey: in.GroupKey,
		Body: strings.TrimSpace(in.Body), Public: in.Public, AuthorID: a.UserID, CreatedAt: now}
	var n int64
	err := tx.Transaction(func(t *gorm.DB) error {
		if err := t.Create(&rep).Error; err != nil {
			return err
		}
		res := t.Model(&model.EduHelpRequest{}).Where("tenant_id = ? AND assignment_id = ? AND group_key = ? AND state = ?", a.TenantID, as.ID, in.GroupKey, "open").
			Updates(map[string]any{"state": "answered", "reply_id": rep.ID, "updated_at": now})
		n = res.RowsAffected
		return res.Error
	})
	if err != nil {
		h.fail(c, err)
		return
	}
	h.Svc.audit(tx, a, "help.reply", as.ID, rep.ID+" group="+in.GroupKey)
	reply(c, 200, gin.H{"reply": rep, "answered": n}, "ok")
}

// dashboard: per-class distribution for one assignment (FR-T12). Individual
// records stay behind GET /assignments/:id/attempts (teaching permission).
func (h *API) dashboard(c *gin.Context) {
	a, tx, as, ok := h.staffAssignment(c)
	if !ok {
		return
	}
	var students []string
	tx.Model(&model.EduEnrollment{}).Where("tenant_id = ? AND class_id = ? AND status = ?", a.TenantID, as.ClassID, "active").Pluck("student_user_id", &students)
	var ats []model.EduAttempt
	tx.Where("tenant_id = ? AND assignment_id = ? AND valid = ?", a.TenantID, as.ID, true).Order("created_at").Find(&ats)
	var hs []model.EduHintUnlock
	tx.Where("tenant_id = ? AND assignment_id = ?", a.TenantID, as.ID).Find(&hs)
	var reports int64
	tx.Model(&model.EduHelpRequest{}).Where("tenant_id = ? AND assignment_id = ? AND state = ? AND priority = ?", a.TenantID, as.ID, "open", 1).Count(&reports)
	var openHelp int64
	tx.Model(&model.EduHelpRequest{}).Where("tenant_id = ? AND assignment_id = ? AND state = ?", a.TenantID, as.ID, "open").Count(&openHelp)
	hinted := map[string]bool{}
	for _, x := range hs {
		hinted[x.StudentUserID] = true
	}
	main := map[string][]model.EduAttempt{}
	check := map[string][]model.EduAttempt{}
	for _, at := range ats {
		if at.Item == "main" {
			main[at.StudentUserID] = append(main[at.StudentUserID], at)
		} else {
			check[at.StudentUserID] = append(check[at.StudentUserID], at)
		}
	}
	submitted := map[string]bool{}
	var sts []model.EduSelfTest
	tx.Where("tenant_id = ? AND assignment_id = ? AND state = ?", as.TenantID, as.ID, "submitted").Find(&sts)
	for _, st := range sts {
		submitted[st.StudentUserID] = true
	}
	dist := map[string]int{"not_started": 0, "attempted": 0, "completed_independently": 0, "completed_after_hints": 0, "pending_teacher": 0}
	selfIndep, selfCorrect := 0, 0
	for _, s := range students {
		list := main[s]
		if len(list) == 0 {
			if hinted[s] {
				dist["attempted"]++
			} else {
				dist["not_started"]++
			}
		} else {
			last := list[len(list)-1]
			solved, pending := false, false
			for _, x := range list {
				r := effective(x)
				solved = solved || r == "correct" || r == "correct_approx"
			}
			r := effective(last)
			pending = r == "needs_teacher" || r == "undetermined"
			switch {
			case pending:
				dist["pending_teacher"]++
			case solved && list[0].FirstValid && list[0].HintLevel == 0 && (effective(list[0]) == "correct" || effective(list[0]) == "correct_approx"):
				dist["completed_independently"]++
			case solved:
				dist["completed_after_hints"]++
			default:
				dist["attempted"]++
			}
		}
		// One self-test unit per student, counted only once the whole unit is submitted.
		if ck := check[s]; len(ck) > 0 && submitted[s] && unitIndependent(ck) {
			selfIndep++
			if unitLabel(ck) == "correct" {
				selfCorrect++
			}
		}
	}
	reply(c, 200, gin.H{"assignmentId": as.ID, "students": len(students), "distribution": dist,
		"selfTest": gin.H{"independent": selfIndep, "independentCorrect": selfCorrect},
		"help":     gin.H{"open": openHelp, "openReports": reports},
		"note":     "distribution only; error causes are suggestions to verify against each student's attempts"}, "ok")
}

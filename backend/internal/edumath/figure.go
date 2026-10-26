package edumath

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// Interactive figures (FR-S05, AC-25/26). The figure spec itself travels in the
// student projection produced by the isolated math service; these endpoints only
// record the student's qualitative prediction (first choice wins) and aggregated
// interaction counts. Neither is ever used as evidence of mastery or of
// mathematical correctness (constraint 4).

const maxFigureEventsPerReport = 500

func (h *API) registerFigure(g *gin.RouterGroup) {
	g.POST("/student/assignments/:id/predictions", h.recordPrediction)
	g.POST("/student/assignments/:id/figure-events", h.recordFigureEvents)
	g.GET("/assignments/:id/figure-summary", h.figureSummary)
}

func (h *API) recordPrediction(c *gin.Context) {
	a, tx, as, pub, ok := h.learner(c)
	if !ok {
		return
	}
	var in struct {
		PromptID string `json:"promptId"`
		Choice   *int   `json:"choice"`
	}
	if c.ShouldBindJSON(&in) != nil || in.PromptID == "" || len(in.PromptID) > 40 || in.Choice == nil {
		reply(c, 400, nil, "promptId and choice are required")
		return
	}
	var prev model.EduPrediction
	if tx.First(&prev, "tenant_id = ? AND assignment_id = ? AND student_user_id = ? AND prompt_id = ?", a.TenantID, as.ID, a.UserID, in.PromptID).Error == nil {
		reply(c, 200, gin.H{"prediction": prev, "replayed": true}, "ok")
		return
	}
	if _, closed := h.closedForWrites(tx, as, a.UserID); closed {
		h.fail(c, appErr(http.StatusConflict, "ASSIGNMENT_CLOSED", "the assignment is closed", "contact_teacher"))
		return
	}
	lesson, err := h.lessonFor(c, tx, a.TenantID, pub)
	if err != nil {
		h.fail(c, err)
		return
	}
	ex := lesson.Figure.Explore
	if ex == nil || ex.Prediction.ID != in.PromptID {
		h.fail(c, appErr(http.StatusNotFound, "PROMPT_NOT_FOUND", "this lesson has no such prediction prompt", "reload"))
		return
	}
	if *in.Choice < 0 || *in.Choice >= len(ex.Prediction.Options) {
		reply(c, 400, nil, "choice is out of range")
		return
	}
	p := model.EduPrediction{ID: flow.NewID("epr_"), TenantID: a.TenantID, AssignmentID: as.ID, StudentUserID: a.UserID, PromptID: in.PromptID,
		Choice: *in.Choice, ChoiceText: ex.Prediction.Options[*in.Choice], BeforeAttempt: !hasValidMain(tx, a.TenantID, as.ID, a.UserID), CreatedAt: h.Svc.now()}
	if tx.Transaction(func(t *gorm.DB) error { return t.Create(&p).Error }) != nil {
		// Concurrent duplicate: the first choice is kept.
		if tx.First(&prev, "tenant_id = ? AND assignment_id = ? AND student_user_id = ? AND prompt_id = ?", a.TenantID, as.ID, a.UserID, in.PromptID).Error != nil {
			reply(c, 500, nil, "could not record prediction")
			return
		}
		reply(c, 200, gin.H{"prediction": prev, "replayed": true}, "ok")
		return
	}
	h.Svc.audit(tx, a, "figure.predict", as.ID, in.PromptID+"="+strconv.Itoa(*in.Choice))
	reply(c, 201, gin.H{"prediction": p, "replayed": false}, "ok")
}

func (h *API) recordFigureEvents(c *gin.Context) {
	a, tx, as, _, ok := h.learner(c)
	if !ok {
		return
	}
	var in struct {
		Drags      int `json:"drags"`
		Keys       int `json:"keys"`
		FormInputs int `json:"formInputs"`
		Resets     int `json:"resets"`
		Fallbacks  int `json:"fallbacks"`
	}
	if c.ShouldBindJSON(&in) != nil {
		reply(c, 400, nil, "invalid body")
		return
	}
	vals := []int{in.Drags, in.Keys, in.FormInputs, in.Resets, in.Fallbacks}
	sum := 0
	for _, v := range vals {
		if v < 0 || v > maxFigureEventsPerReport {
			reply(c, 400, nil, "each count must be 0.."+strconv.Itoa(maxFigureEventsPerReport))
			return
		}
		sum += v
	}
	if sum == 0 {
		reply(c, 200, gin.H{"recorded": false}, "ok")
		return
	}
	now := h.Svc.now()
	where := "tenant_id = ? AND assignment_id = ? AND student_user_id = ?"
	upd := func(t *gorm.DB) *gorm.DB {
		return t.Model(&model.EduFigureInteraction{}).Where(where, a.TenantID, as.ID, a.UserID).Updates(map[string]any{
			"drags": gorm.Expr("drags + ?", in.Drags), "key_presses": gorm.Expr("key_presses + ?", in.Keys), "form_inputs": gorm.Expr("form_inputs + ?", in.FormInputs),
			"resets": gorm.Expr("resets + ?", in.Resets), "fallbacks": gorm.Expr("fallbacks + ?", in.Fallbacks), "reports": gorm.Expr("reports + 1"), "updated_at": now})
	}
	res := upd(tx)
	if res.Error != nil {
		h.fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		row := model.EduFigureInteraction{ID: flow.NewID("efi_"), TenantID: a.TenantID, AssignmentID: as.ID, StudentUserID: a.UserID,
			Drags: in.Drags, KeyPresses: in.Keys, FormInputs: in.FormInputs, Resets: in.Resets, Fallbacks: in.Fallbacks, Reports: 1, UpdatedAt: now}
		if tx.Transaction(func(t *gorm.DB) error { return t.Create(&row).Error }) != nil {
			if err := upd(tx).Error; err != nil {
				h.fail(c, err)
				return
			}
		}
	}
	reply(c, 200, gin.H{"recorded": true}, "ok")
}

// figureSummary is the class-level view for teachers: prediction distribution
// and how many students used the interactive figure. It is reported separately
// from mastery and never feeds the learning distribution.
func (h *API) figureSummary(c *gin.Context) {
	a, tx, as, ok := h.staffAssignment(c)
	if !ok {
		return
	}
	var preds []model.EduPrediction
	tx.Where("tenant_id = ? AND assignment_id = ?", a.TenantID, as.ID).Find(&preds)
	type opt struct {
		Choice int    `json:"choice"`
		Text   string `json:"text"`
		Count  int    `json:"count"`
	}
	byPrompt := map[string]map[int]*opt{}
	for _, p := range preds {
		if byPrompt[p.PromptID] == nil {
			byPrompt[p.PromptID] = map[int]*opt{}
		}
		o := byPrompt[p.PromptID][p.Choice]
		if o == nil {
			o = &opt{Choice: p.Choice, Text: p.ChoiceText}
			byPrompt[p.PromptID][p.Choice] = o
		}
		o.Count++
	}
	prompts := gin.H{}
	for id, m := range byPrompt {
		list := []opt{}
		for i := 0; i < 32; i++ {
			if o, ok := m[i]; ok {
				list = append(list, *o)
			}
		}
		prompts[id] = list
	}
	var uses []model.EduFigureInteraction
	tx.Where("tenant_id = ? AND assignment_id = ?", a.TenantID, as.ID).Find(&uses)
	used, keyboardOrForm, fallbacks := 0, 0, 0
	for _, u := range uses {
		if u.Drags+u.KeyPresses+u.FormInputs+u.Resets > 0 {
			used++
		}
		if u.KeyPresses+u.FormInputs > 0 {
			keyboardOrForm++
		}
		fallbacks += u.Fallbacks
	}
	reply(c, 200, gin.H{"assignmentId": as.ID, "predictions": prompts, "figure": gin.H{"studentsUsed": used, "studentsKeyboardOrForm": keyboardOrForm, "fallbacks": fallbacks},
		"note": "predictions and figure use are learning telemetry, not evidence of mastery"}, "ok")
}

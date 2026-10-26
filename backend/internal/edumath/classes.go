package edumath

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// Classes, teaching assignments, invites/join, assignments (tasks), guardian
// consent and org policy (W1: FR-I01–I05, FR-T09/T10 data side). Every rule is
// enforced here on the server; the UI only reflects it.

const (
	maxInviteTTL  = 14 * 24 * time.Hour
	maxInviteUses = 200
)

func consentMode(tx *gorm.DB, tenantID string) string {
	var p model.EduOrgPolicy
	if tx.First(&p, "tenant_id = ?", tenantID).Error == nil && p.ConsentMode == "not_applicable" {
		return "not_applicable"
	}
	return "guardian_required"
}

// consentOK is the FR-I05 gate for every student learning access.
func consentOK(tx *gorm.DB, tenantID, studentID string) bool {
	if consentMode(tx, tenantID) == "not_applicable" {
		return true
	}
	var n int64
	tx.Model(&model.EduConsentRecord{}).Where("tenant_id = ? AND student_user_id = ? AND kind = ? AND status = ?", tenantID, studentID, "guardian", "granted").Count(&n)
	return n > 0
}

// classForStaff loads an active class in the actor's tenant+workspace that the
// actor teaches (active teaching assignment) or administers (org_admin).
func classForStaff(tx *gorm.DB, a Actor, classID string) (model.EduClass, error) {
	var k model.EduClass
	if err := tx.First(&k, "id = ? AND tenant_id = ? AND workspace_id = ? AND status = ?", classID, a.TenantID, a.WorkspaceID, "active").Error; err != nil {
		return k, ErrNotFound
	}
	if a.Has("org_admin") {
		return k, nil
	}
	if !a.Has("teacher") {
		return k, ErrForbidden
	}
	var n int64
	tx.Model(&model.EduTeachingAssignment{}).Where("tenant_id = ? AND class_id = ? AND user_id = ? AND status = ?", a.TenantID, classID, a.UserID, "active").Count(&n)
	if n == 0 {
		return k, ErrNotFound // other teachers' classes are invisible
	}
	return k, nil
}

func (h *API) registerClasses(g *gin.RouterGroup) {
	g.GET("/classes", h.listClasses)
	g.POST("/classes", h.createClass)
	g.POST("/classes/:id/teachers", h.addTeacher)
	g.POST("/classes/:id/invites", h.createInvite)
	g.POST("/classes/:id/invites/:inviteId/revoke", h.revokeInvite)
	g.GET("/classes/:id/roster", h.roster)
	g.POST("/enrollments/:id/withdraw", h.withdrawEnrollment)
	g.POST("/classes/:id/assignments", h.createAssignment)
	g.GET("/classes/:id/assignments", h.listAssignments)
	g.POST("/assignments/:id/withdraw", h.withdrawAssignment)
	g.POST("/consents", h.recordConsent)
	g.PUT("/policy", h.setPolicy)
	g.GET("/policy", h.getPolicy)
}

func (h *API) listClasses(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var ks []model.EduClass
	q := tx.Where("tenant_id = ? AND workspace_id = ? AND status = ?", a.TenantID, a.WorkspaceID, "active")
	if !a.Has("org_admin") {
		q = q.Where("id IN (?)", tx.Model(&model.EduTeachingAssignment{}).Select("class_id").Where("tenant_id = ? AND user_id = ? AND status = ?", a.TenantID, a.UserID, "active"))
	}
	q.Order("created_at").Find(&ks)
	reply(c, 200, ks, "ok")
}

func (h *API) createClass(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	if !a.Has("teacher", "org_admin") {
		h.fail(c, ErrForbidden)
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if c.ShouldBindJSON(&in) != nil || strings.TrimSpace(in.Name) == "" || len(in.Name) > 120 {
		reply(c, 400, nil, "name required (≤120)")
		return
	}
	now := h.Svc.now()
	k := model.EduClass{ID: flow.NewID("ecl_"), TenantID: a.TenantID, WorkspaceID: a.WorkspaceID, Name: strings.TrimSpace(in.Name), Status: "active", CreatedBy: a.UserID, CreatedAt: now}
	if err := tx.Create(&k).Error; err != nil {
		h.fail(c, err)
		return
	}
	if a.Has("teacher") {
		if err := tx.Create(&model.EduTeachingAssignment{ID: flow.NewID("eta_"), TenantID: a.TenantID, ClassID: k.ID, UserID: a.UserID, Status: "active", CreatedAt: now}).Error; err != nil {
			h.fail(c, err)
			return
		}
	}
	h.Svc.audit(tx, a, "class.create", k.ID, k.Name)
	reply(c, 201, k, "ok")
}

// addTeacher: org admin (or tenant owner/admin) assigns a teacher who holds the
// teacher role in this tenant and is a member of the class workspace.
func (h *API) addTeacher(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	if !canManageRoles(tx, a.TenantID, a.UserID) {
		h.fail(c, ErrForbidden)
		return
	}
	k, err := classForStaff(tx, Actor{UserID: a.UserID, TenantID: a.TenantID, WorkspaceID: a.WorkspaceID, Roles: map[string]bool{"org_admin": true}}, c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	var in struct {
		UserID string `json:"userId"`
	}
	if c.ShouldBindJSON(&in) != nil || in.UserID == "" {
		reply(c, 400, nil, "userId required")
		return
	}
	rs, _ := roles(tx, a.TenantID, in.UserID)
	var m int64
	tx.Model(&model.WorkspaceMembership{}).Where("workspace_id = ? AND user_id = ?", k.WorkspaceID, in.UserID).Count(&m)
	if !rs["teacher"] || m == 0 {
		reply(c, 400, nil, "user must hold the teacher role and be a member of the class workspace")
		return
	}
	ta := model.EduTeachingAssignment{ID: flow.NewID("eta_"), TenantID: a.TenantID, ClassID: k.ID, UserID: in.UserID, Status: "active", CreatedAt: h.Svc.now()}
	if err := tx.Where("tenant_id = ? AND class_id = ? AND user_id = ?", a.TenantID, k.ID, in.UserID).FirstOrCreate(&ta).Error; err != nil {
		h.fail(c, err)
		return
	}
	if ta.Status != "active" {
		tx.Model(&ta).Update("status", "active")
		ta.Status = "active"
	}
	h.Svc.audit(tx, a, "class.teacher.add", k.ID, in.UserID)
	reply(c, 200, ta, "ok")
}

func secretHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (h *API) createInvite(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	k, err := classForStaff(tx, a, c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	var in struct {
		TTLHours int `json:"ttlHours"`
		MaxUses  int `json:"maxUses"`
	}
	_ = c.ShouldBindJSON(&in)
	ttl := time.Duration(in.TTLHours) * time.Hour
	if ttl <= 0 || ttl > maxInviteTTL {
		ttl = 7 * 24 * time.Hour
	}
	if in.MaxUses <= 0 || in.MaxUses > maxInviteUses {
		in.MaxUses = 60
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		h.fail(c, err)
		return
	}
	secret := hex.EncodeToString(buf)
	now := h.Svc.now()
	inv := model.EduClassInvite{ID: flow.NewID("eci_"), TenantID: a.TenantID, ClassID: k.ID, SecretHash: secretHash(secret), ExpiresAt: now.Add(ttl), MaxUses: in.MaxUses, Status: "active", CreatedBy: a.UserID, CreatedAt: now}
	if err := tx.Create(&inv).Error; err != nil {
		h.fail(c, err)
		return
	}
	h.Svc.audit(tx, a, "class.invite.create", k.ID, inv.ID)
	reply(c, 201, gin.H{"invite": inv, "code": inv.ID + "." + secret}, "ok")
}

func (h *API) revokeInvite(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	k, err := classForStaff(tx, a, c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	res := tx.Model(&model.EduClassInvite{}).Where("id = ? AND tenant_id = ? AND class_id = ?", c.Param("inviteId"), a.TenantID, k.ID).Update("status", "revoked")
	if res.RowsAffected == 0 {
		h.fail(c, ErrNotFound)
		return
	}
	h.Svc.audit(tx, a, "class.invite.revoke", k.ID, c.Param("inviteId"))
	reply(c, 200, gin.H{"revoked": true}, "ok")
}

// join is mounted outside the tenant-scoped middleware: the joining user is not
// yet in the class tenant. The invite is located by id in system context, the
// secret is checked in constant time, and the writes run in TenantTx(invite tenant).
// It grants the student role + class enrollment only; never a workspace membership.
func (h *API) join(c *gin.Context) {
	u, err := h.User(c)
	if err != nil || u == nil {
		reply(c, 401, nil, "login required")
		return
	}
	var in struct {
		Code string `json:"code"`
	}
	if c.ShouldBindJSON(&in) != nil {
		reply(c, 400, nil, "code required")
		return
	}
	id, secret, found := strings.Cut(strings.TrimSpace(in.Code), ".")
	if !found || !strings.HasPrefix(id, "eci_") || len(secret) != 48 {
		reply(c, 404, nil, "invalid or expired invite")
		return
	}
	var inv model.EduClassInvite
	if err := h.DB.First(&inv, "id = ?", id).Error; err != nil ||
		subtle.ConstantTimeCompare([]byte(inv.SecretHash), []byte(secretHash(secret))) != 1 {
		reply(c, 404, nil, "invalid or expired invite")
		return
	}
	now := h.Svc.now()
	var enr model.EduEnrollment
	errBlocked := errors.New("blocked")
	err = flow.TenantTx(h.DB, inv.TenantID, func(tx *gorm.DB) error {
		var k model.EduClass
		if tx.First(&k, "id = ? AND tenant_id = ? AND status = ?", inv.ClassID, inv.TenantID, "active").Error != nil {
			return ErrNotFound
		}
		// Existing enrollment: idempotent if active; a withdrawn student needs the teacher, not a code.
		if tx.First(&enr, "tenant_id = ? AND class_id = ? AND student_user_id = ?", inv.TenantID, inv.ClassID, u.ID).Error == nil {
			if enr.Status != "active" {
				return errBlocked
			}
			return nil
		}
		res := tx.Model(&model.EduClassInvite{}).Where("id = ? AND status = ? AND uses < max_uses AND expires_at > ?", inv.ID, "active", now).
			Update("uses", gorm.Expr("uses + 1"))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrNotFound
		}
		var rb model.EduRoleBinding
		if tx.First(&rb, "tenant_id = ? AND user_id = ? AND role = ?", inv.TenantID, u.ID, "student").Error == nil {
			if rb.Status != "active" {
				return errBlocked // revoked by the institution; an invite cannot undo it
			}
		} else if err := tx.Create(&model.EduRoleBinding{ID: flow.NewID("erb_"), TenantID: inv.TenantID, UserID: u.ID, Role: "student", Status: "active", CreatedBy: "invite:" + inv.ID, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			return err
		}
		enr = model.EduEnrollment{ID: flow.NewID("een_"), TenantID: inv.TenantID, ClassID: inv.ClassID, StudentUserID: u.ID, Status: "active", CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&enr).Error; err != nil {
			return err
		}
		h.Svc.audit(tx, Actor{UserID: u.ID, TenantID: inv.TenantID}, "class.join", inv.ClassID, inv.ID)
		return nil
	})
	switch {
	case errors.Is(err, errBlocked):
		reply(c, 403, gin.H{"error": gin.H{"code": "POLICY_REVOKED", "next_action": "contact_teacher"}}, "enrollment blocked; contact the teacher")
	case err != nil:
		reply(c, 404, nil, "invalid or expired invite")
	default:
		reply(c, 200, gin.H{"tenantId": inv.TenantID, "classId": inv.ClassID, "enrollmentId": enr.ID, "consentRequired": !consentOKSystem(h.DB, inv.TenantID, u.ID)}, "ok")
	}
}

func consentOKSystem(db *gorm.DB, tenantID, userID string) bool {
	ok := false
	_ = flow.TenantTx(db, tenantID, func(tx *gorm.DB) error { ok = consentOK(tx, tenantID, userID); return nil })
	return ok
}

// roster returns only what a teacher of the class needs: enrollment ids, the
// student user id, status and consent state. No other class's records.
func (h *API) roster(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	k, err := classForStaff(tx, a, c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	var es []model.EduEnrollment
	tx.Where("tenant_id = ? AND class_id = ?", a.TenantID, k.ID).Order("created_at").Find(&es)
	out := []gin.H{}
	for _, e := range es {
		out = append(out, gin.H{"enrollmentId": e.ID, "studentUserId": e.StudentUserID, "status": e.Status, "consentOk": consentOK(tx, a.TenantID, e.StudentUserID)})
	}
	reply(c, 200, out, "ok")
}

func (h *API) withdrawEnrollment(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var e model.EduEnrollment
	if tx.First(&e, "id = ? AND tenant_id = ?", c.Param("id"), a.TenantID).Error != nil {
		h.fail(c, ErrNotFound)
		return
	}
	if _, err := classForStaff(tx, a, e.ClassID); err != nil {
		h.fail(c, err)
		return
	}
	tx.Model(&e).Updates(map[string]any{"status": "withdrawn", "updated_at": h.Svc.now()})
	h.Svc.audit(tx, a, "class.enrollment.withdraw", e.ClassID, e.ID)
	reply(c, 200, gin.H{"id": e.ID, "status": "withdrawn"}, "ok")
}

// createAssignment releases a published, immutable publication to one class.
func (h *API) createAssignment(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	k, err := classForStaff(tx, a, c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	var in struct {
		PublicationID string     `json:"publicationId"`
		Title         string     `json:"title"`
		StartsAt      *time.Time `json:"startsAt"` // RFC 3339 with an explicit offset; stored in UTC
		DueAt         *time.Time `json:"dueAt"`
		AllowLate     bool       `json:"allowLate"`
		AnswerPolicy  string     `json:"answerPolicy"`
		ApproxAllowed bool       `json:"approxAllowed"`
		UnitPolicy    string     `json:"unitPolicy"`
		VariantSetID  string     `json:"variantSetId"`
	}
	if c.ShouldBindJSON(&in) != nil || in.PublicationID == "" || len(in.Title) > 200 {
		reply(c, 400, nil, "publicationId required")
		return
	}
	if in.AnswerPolicy == "" {
		in.AnswerPolicy = "teacher_release"
	}
	if in.UnitPolicy == "" {
		in.UnitPolicy = "required"
	}
	if !map[string]bool{"after_submission": true, "after_due": true, "teacher_release": true}[in.AnswerPolicy] ||
		(in.UnitPolicy != "required" && in.UnitPolicy != "optional") ||
		(in.AnswerPolicy == "after_due" && in.DueAt == nil) ||
		(in.StartsAt != nil && in.DueAt != nil && !in.DueAt.After(*in.StartsAt)) {
		reply(c, 400, nil, "invalid assignment policy")
		return
	}
	utc := func(t *time.Time) *time.Time {
		if t == nil {
			return nil
		}
		u := t.UTC()
		return &u
	}
	var pub model.EduPublication
	if tx.First(&pub, "id = ? AND tenant_id = ? AND state = ?", in.PublicationID, a.TenantID, "published").Error != nil {
		h.fail(c, appErr(409, "REVISION_CONFLICT", "only a published lesson can be assigned", "publish_first"))
		return
	}
	// The publication must come from the same workspace as the class.
	var l model.EduLessonRevision
	var job model.EduMathJob
	if tx.First(&l, "id = ? AND tenant_id = ?", pub.LessonRevisionID, a.TenantID).Error != nil ||
		tx.First(&job, "id = ? AND tenant_id = ?", l.JobID, a.TenantID).Error != nil || job.WorkspaceID != k.WorkspaceID {
		h.fail(c, ErrNotFound)
		return
	}
	if in.VariantSetID != "" {
		if _, err := approvedVariantSetFor(tx, a, in.VariantSetID, pub); err != nil {
			h.fail(c, err)
			return
		}
	}
	as := model.EduAssignment{ID: flow.NewID("eas_"), TenantID: a.TenantID, ClassID: k.ID, PublicationID: pub.ID, Title: strings.TrimSpace(in.Title), State: "active", CreatedBy: a.UserID, CreatedAt: h.Svc.now(),
		StartsAt: utc(in.StartsAt), DueAt: utc(in.DueAt), AllowLate: in.AllowLate, AnswerPolicy: in.AnswerPolicy, ApproxAllowed: in.ApproxAllowed, UnitPolicy: in.UnitPolicy,
		VariantSetID: in.VariantSetID}
	if err := tx.Create(&as).Error; err != nil {
		h.fail(c, err)
		return
	}
	h.Svc.audit(tx, a, "assignment.create", k.ID, as.ID+" pub="+pub.ID)
	reply(c, 201, as, "ok")
}

func (h *API) listAssignments(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	k, err := classForStaff(tx, a, c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	var as []model.EduAssignment
	tx.Where("tenant_id = ? AND class_id = ?", a.TenantID, k.ID).Order("created_at desc").Find(&as)
	reply(c, 200, as, "ok")
}

func (h *API) withdrawAssignment(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	var as model.EduAssignment
	if tx.First(&as, "id = ? AND tenant_id = ?", c.Param("id"), a.TenantID).Error != nil {
		h.fail(c, ErrNotFound)
		return
	}
	if _, err := classForStaff(tx, a, as.ClassID); err != nil {
		h.fail(c, err)
		return
	}
	tx.Model(&as).Update("state", "withdrawn")
	h.Svc.audit(tx, a, "assignment.withdraw", as.ClassID, as.ID)
	reply(c, 200, gin.H{"id": as.ID, "state": "withdrawn"}, "ok")
}

// recordConsent: only an org admin (or tenant owner/admin) with an evidence
// reference; teachers cannot create or fake consent (AC-04).
func (h *API) recordConsent(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	if !canManageRoles(tx, a.TenantID, a.UserID) {
		h.fail(c, ErrForbidden)
		return
	}
	var in struct {
		StudentUserID string `json:"studentUserId"`
		Status        string `json:"status"`
		EvidenceRef   string `json:"evidenceRef"`
	}
	if c.ShouldBindJSON(&in) != nil || in.StudentUserID == "" || (in.Status != "granted" && in.Status != "withdrawn") {
		reply(c, 400, nil, "studentUserId and status (granted|withdrawn) required")
		return
	}
	if in.Status == "granted" && (len(strings.TrimSpace(in.EvidenceRef)) < 4 || len(in.EvidenceRef) > 300) {
		reply(c, 400, nil, "evidenceRef (signed form id, document reference) required to grant")
		return
	}
	rs, _ := roles(tx, a.TenantID, in.StudentUserID)
	if !rs["student"] {
		h.fail(c, ErrNotFound)
		return
	}
	now := h.Svc.now()
	var rec model.EduConsentRecord
	ev := strings.TrimSpace(in.EvidenceRef)
	if tx.First(&rec, "tenant_id = ? AND student_user_id = ? AND kind = ?", a.TenantID, in.StudentUserID, "guardian").Error != nil {
		if in.Status != "granted" {
			h.fail(c, ErrNotFound)
			return
		}
		rec = model.EduConsentRecord{ID: flow.NewID("ecr_"), TenantID: a.TenantID, StudentUserID: in.StudentUserID, Kind: "guardian", Status: in.Status, EvidenceRef: ev, RecordedBy: a.UserID, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&rec).Error; err != nil {
			h.fail(c, err)
			return
		}
	} else {
		if ev == "" {
			ev = rec.EvidenceRef
		}
		if err := tx.Model(&model.EduConsentRecord{}).Where("id = ?", rec.ID).Updates(map[string]any{"status": in.Status, "evidence_ref": ev, "recorded_by": a.UserID, "updated_at": now}).Error; err != nil {
			h.fail(c, err)
			return
		}
	}
	h.Svc.audit(tx, a, "consent."+in.Status, in.StudentUserID, ev)
	reply(c, 200, gin.H{"studentUserId": in.StudentUserID, "status": in.Status}, "ok")
}

func (h *API) getPolicy(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	reply(c, 200, gin.H{"reviewMode": h.Svc.policy(tx, a.TenantID), "consentMode": consentMode(tx, a.TenantID)}, "ok")
}

func (h *API) setPolicy(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	if !canManageRoles(tx, a.TenantID, a.UserID) {
		h.fail(c, ErrForbidden)
		return
	}
	var in struct {
		ReviewMode  string `json:"reviewMode"`
		ConsentMode string `json:"consentMode"`
		Reason      string `json:"reason"`
	}
	if c.ShouldBindJSON(&in) != nil || (in.ReviewMode != "self_allowed" && in.ReviewMode != "second_reviewer") ||
		(in.ConsentMode != "guardian_required" && in.ConsentMode != "not_applicable") {
		reply(c, 400, nil, "reviewMode (self_allowed|second_reviewer) and consentMode (guardian_required|not_applicable) required")
		return
	}
	if in.ConsentMode == "not_applicable" && len(strings.TrimSpace(in.Reason)) < 4 {
		reply(c, 400, nil, "reason required when consent is marked not applicable")
		return
	}
	p := model.EduOrgPolicy{TenantID: a.TenantID, ReviewMode: in.ReviewMode, ConsentMode: in.ConsentMode, UpdatedBy: a.UserID, UpdatedAt: h.Svc.now()}
	if err := tx.Save(&p).Error; err != nil {
		h.fail(c, err)
		return
	}
	h.Svc.audit(tx, a, "policy.set", a.TenantID, in.ReviewMode+"/"+in.ConsentMode+" "+in.Reason)
	reply(c, http.StatusOK, p, "ok")
}

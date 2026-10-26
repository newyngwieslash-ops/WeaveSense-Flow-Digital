package edumath

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// Minimal roster import (DEV-016). A teacher of the class uploads CSV text with
// a pseudonymous external reference (and an optional display name); preview
// first, then confirm. Each new row gets a single-use claim code, returned once
// and stored hashed, that expires (default 14 days). A student signs in to the
// platform and claims the code: that binds the user to the row and enrolls them
// in the class (student role only, never a workspace membership). Values that
// look like phone numbers, ID card numbers or e-mail addresses are refused so
// the roster stays minimal.

const maxRosterRows = 500

var (
	rosterRefRe  = regexp.MustCompile(`^[A-Za-z0-9_\-]{1,40}$`)
	piiPhoneRe   = regexp.MustCompile(`1[3-9]\d{9}`)
	piiIDCardRe  = regexp.MustCompile(`\d{17}[\dXx]`)
	rosterHeader = map[string]string{"externalref": "ref", "ref": "ref", "学号": "ref", "编号": "ref",
		"displayname": "name", "name": "name", "显示名": "name", "昵称": "name"}
)

func (h *API) registerRoster(g *gin.RouterGroup) {
	g.POST("/classes/:id/roster-import", h.rosterImport)
	g.GET("/classes/:id/roster-entries", h.rosterEntries)
	g.POST("/classes/:id/roster-entries/:eid/reissue", h.rosterReissue)
	g.POST("/classes/:id/roster-entries/:eid/revoke", h.rosterRevoke)
}

type rosterRow struct {
	Line   int    `json:"line"`
	Ref    string `json:"externalRef"`
	Name   string `json:"displayName"`
	Status string `json:"status"` // new | existing | invalid
	Error  string `json:"error,omitempty"`
}

func parseRoster(text string) ([]rosterRow, error) {
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(text, "\ufeff")))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	head, err := r.Read()
	if err != nil {
		return nil, errors.New("empty csv")
	}
	col := map[string]int{}
	for i, hname := range head {
		if k, ok := rosterHeader[strings.ToLower(strings.TrimSpace(hname))]; ok {
			col[k] = i
		} else {
			return nil, errors.New("unknown column " + strings.TrimSpace(hname) + "; only externalRef and displayName are accepted")
		}
	}
	if _, ok := col["ref"]; !ok {
		return nil, errors.New("externalRef column is required")
	}
	var rows []rosterRow
	seen := map[string]bool{}
	for line := 2; ; line++ {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("malformed csv")
		}
		if len(rows) >= maxRosterRows {
			return nil, errors.New("too many rows")
		}
		get := func(k string) string {
			if i, ok := col[k]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		row := rosterRow{Line: line, Ref: get("ref"), Name: get("name"), Status: "new"}
		if row.Ref == "" && row.Name == "" {
			continue
		}
		switch {
		case !rosterRefRe.MatchString(row.Ref):
			row.Status, row.Error = "invalid", "externalRef must be 1..40 letters, digits, _ or -"
		case len([]rune(row.Name)) > 20:
			row.Status, row.Error = "invalid", "displayName at most 20 chars"
		case looksPII(row.Ref) || looksPII(row.Name):
			row.Status, row.Error = "invalid", "looks like a phone, ID card or e-mail; use a pseudonymous reference"
		case seen[row.Ref]:
			row.Status, row.Error = "invalid", "duplicate externalRef in file"
		}
		seen[row.Ref] = true
		rows = append(rows, row)
	}
	return rows, nil
}

func looksPII(v string) bool {
	return piiPhoneRe.MatchString(v) || piiIDCardRe.MatchString(v) || strings.Contains(v, "@")
}

func newClaimSecret() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func claimTTL(hours int) time.Duration {
	ttl := time.Duration(hours) * time.Hour
	if ttl <= 0 || ttl > maxInviteTTL {
		ttl = maxInviteTTL
	}
	return ttl
}

func (h *API) rosterImport(c *gin.Context) {
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
		CSV      string `json:"csv"`
		TTLHours int    `json:"ttlHours"`
		Confirm  bool   `json:"confirm"`
	}
	if c.ShouldBindJSON(&in) != nil || len(in.CSV) > 64*1024 {
		h.fail(c, appErr(400, "VALIDATION_FAILED", "csv text up to 64 KiB is required", "fix_input"))
		return
	}
	rows, perr := parseRoster(in.CSV)
	if perr != nil {
		h.fail(c, appErr(400, "VALIDATION_FAILED", perr.Error(), "fix_input"))
		return
	}
	counts := map[string]int{}
	for i := range rows {
		if rows[i].Status == "new" {
			var n int64
			tx.Model(&model.EduRosterEntry{}).Where("tenant_id = ? AND class_id = ? AND external_ref = ?", a.TenantID, k.ID, rows[i].Ref).Count(&n)
			if n > 0 {
				rows[i].Status = "existing"
			}
		}
		counts[rows[i].Status]++
	}
	out := gin.H{"classId": k.ID, "rows": rows, "new": counts["new"], "existing": counts["existing"], "invalid": counts["invalid"], "confirmed": false}
	if !in.Confirm {
		reply(c, 200, out, "ok")
		return
	}
	if counts["invalid"] > 0 {
		h.fail(c, appErr(409, "VALIDATION_FAILED", "fix the invalid rows before confirming", "fix_input"))
		return
	}
	batch := flow.NewID("erb_imp_")
	now := h.Svc.now()
	exp := now.Add(claimTTL(in.TTLHours))
	codes := []gin.H{}
	for _, r := range rows {
		if r.Status != "new" {
			continue
		}
		secret, err := newClaimSecret()
		if err != nil {
			h.fail(c, err)
			return
		}
		e := model.EduRosterEntry{ID: flow.NewID("erc_"), TenantID: a.TenantID, ClassID: k.ID, ExternalRef: r.Ref, DisplayName: r.Name, State: "pending",
			SecretHash: secretHash(secret), ExpiresAt: exp, ImportBatch: batch, CreatedBy: a.UserID, CreatedAt: now}
		if err := tx.Create(&e).Error; err != nil {
			h.fail(c, err)
			return
		}
		codes = append(codes, gin.H{"entryId": e.ID, "externalRef": e.ExternalRef, "code": e.ID + "." + secret, "expiresAt": exp})
	}
	h.Svc.audit(tx, a, "class.roster.import", k.ID, batch)
	out["confirmed"], out["batch"], out["codes"] = true, batch, codes
	reply(c, 201, out, "ok")
}

func (h *API) rosterEntries(c *gin.Context) {
	a, tx, ok := h.staff(c)
	if !ok {
		return
	}
	k, err := classForStaff(tx, a, c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	var es []model.EduRosterEntry
	tx.Where("tenant_id = ? AND class_id = ?", a.TenantID, k.ID).Order("created_at, external_ref").Limit(2000).Find(&es)
	reply(c, 200, es, "ok")
}

func (h *API) rosterEntry(c *gin.Context) (Actor, *gorm.DB, model.EduRosterEntry, bool) {
	var e model.EduRosterEntry
	a, tx, ok := h.staff(c)
	if !ok {
		return a, tx, e, false
	}
	k, err := classForStaff(tx, a, c.Param("id"))
	if err == nil && tx.First(&e, "id = ? AND tenant_id = ? AND class_id = ?", c.Param("eid"), a.TenantID, k.ID).Error != nil {
		err = ErrNotFound
	}
	if err != nil {
		h.fail(c, err)
		return a, tx, e, false
	}
	return a, tx, e, true
}

func (h *API) rosterReissue(c *gin.Context) {
	a, tx, e, ok := h.rosterEntry(c)
	if !ok {
		return
	}
	if e.State != "pending" {
		h.fail(c, appErr(409, "CONFLICT", "only an unclaimed entry can get a new code", "none"))
		return
	}
	var in struct {
		TTLHours int `json:"ttlHours"`
	}
	_ = c.ShouldBindJSON(&in)
	secret, err := newClaimSecret()
	if err != nil {
		h.fail(c, err)
		return
	}
	exp := h.Svc.now().Add(claimTTL(in.TTLHours))
	if err := tx.Model(&model.EduRosterEntry{}).Where("id = ? AND state = 'pending'", e.ID).Updates(map[string]any{"secret_hash": secretHash(secret), "expires_at": exp}).Error; err != nil {
		h.fail(c, err)
		return
	}
	h.Svc.audit(tx, a, "class.roster.reissue", e.ClassID, e.ID)
	reply(c, 200, gin.H{"entryId": e.ID, "code": e.ID + "." + secret, "expiresAt": exp}, "ok")
}

func (h *API) rosterRevoke(c *gin.Context) {
	a, tx, e, ok := h.rosterEntry(c)
	if !ok {
		return
	}
	if e.State != "pending" {
		h.fail(c, appErr(409, "CONFLICT", "a claimed entry is removed by withdrawing the enrollment", "withdraw_enrollment"))
		return
	}
	tx.Model(&model.EduRosterEntry{}).Where("id = ?", e.ID).Updates(map[string]any{"state": "revoked", "secret_hash": ""})
	h.Svc.audit(tx, a, "class.roster.revoke", e.ClassID, e.ID)
	reply(c, 200, gin.H{"revoked": true}, "ok")
}

// claimRoster is mounted outside the tenant middleware like join: the entry is
// located by id in system context, the secret compared in constant time, and the
// writes run in TenantTx(entry tenant). Single use; expired, revoked or claimed
// codes all answer 404.
func (h *API) claimRoster(c *gin.Context) {
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
	if !found || !strings.HasPrefix(id, "erc_") || len(secret) != 48 {
		reply(c, 404, nil, "invalid or expired code")
		return
	}
	var e model.EduRosterEntry
	if err := h.DB.First(&e, "id = ?", id).Error; err != nil || e.SecretHash == "" ||
		subtle.ConstantTimeCompare([]byte(e.SecretHash), []byte(secretHash(secret))) != 1 {
		reply(c, 404, nil, "invalid or expired code")
		return
	}
	now := h.Svc.now()
	var enr model.EduEnrollment
	err = flow.TenantTx(h.DB, e.TenantID, func(tx *gorm.DB) error {
		var k model.EduClass
		if tx.First(&k, "id = ? AND tenant_id = ? AND status = ?", e.ClassID, e.TenantID, "active").Error != nil {
			return ErrNotFound
		}
		var other int64
		tx.Model(&model.EduRosterEntry{}).Where("tenant_id = ? AND class_id = ? AND claimed_by = ?", e.TenantID, e.ClassID, u.ID).Count(&other)
		if other > 0 {
			return errEnrollBlocked // one roster row per student per class
		}
		if tx.First(&enr, "tenant_id = ? AND class_id = ? AND student_user_id = ?", e.TenantID, e.ClassID, u.ID).Error == nil {
			if enr.Status != "active" {
				return errEnrollBlocked
			}
		} else {
			var err error
			if enr, err = enrollStudent(tx, e.TenantID, e.ClassID, u.ID, "roster:"+e.ID, now); err != nil {
				return err
			}
		}
		res := tx.Model(&model.EduRosterEntry{}).Where("id = ? AND state = 'pending' AND expires_at > ?", e.ID, now).
			Updates(map[string]any{"state": "claimed", "claimed_by": u.ID, "claimed_at": now, "secret_hash": ""})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrNotFound
		}
		h.Svc.audit(tx, Actor{UserID: u.ID, TenantID: e.TenantID}, "class.roster.claim", e.ClassID, e.ID)
		return nil
	})
	switch {
	case errors.Is(err, errEnrollBlocked):
		reply(c, 403, gin.H{"error": gin.H{"code": "POLICY_REVOKED", "next_action": "contact_teacher"}}, "claim blocked; contact the teacher")
	case err != nil:
		reply(c, 404, nil, "invalid or expired code")
	default:
		reply(c, 200, gin.H{"tenantId": e.TenantID, "classId": e.ClassID, "enrollmentId": enr.ID, "externalRef": e.ExternalRef,
			"consentRequired": !consentOKSystem(h.DB, e.TenantID, u.ID)}, "ok")
	}
}

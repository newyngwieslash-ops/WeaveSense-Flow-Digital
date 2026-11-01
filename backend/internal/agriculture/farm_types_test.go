package agriculture

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"yingce/backend/internal/model"
)

func TestFarmPayloadClosedAndExplicitGrants(t *testing.T) {
	valid := FarmPayload{Action: "create", Farm: json.RawMessage(`{"external_code":"F1","name":"Farm"}`), InitialGrants: []FarmGrant{{SubjectUserID: "actor_a", Capabilities: []string{"agri.farm.read"}}}}
	if e := validateFarmPayload(&valid); e != nil {
		t.Fatal(e)
	}
	for name, mutate := range map[string]func(*FarmPayload){
		"no_grants":            func(p *FarmPayload) { p.InitialGrants = nil },
		"self_asserted_tenant": func(p *FarmPayload) { p.Farm = json.RawMessage(`{"tenant_id":"foreign"}`) },
		"arbitrary_capability": func(p *FarmPayload) {
			p.InitialGrants = []FarmGrant{{SubjectUserID: "a", Capabilities: []string{"agri.billing.update"}}}
		},
		"null_contact":    func(p *FarmPayload) { p.Farm = json.RawMessage(`{"contact":null}`) },
		"create_revision": func(p *FarmPayload) { p.ExpectedRevision = 1 },
		"create_id":       func(p *FarmPayload) { p.FarmID = "farm_other" },
	} {
		t.Run(name, func(t *testing.T) {
			value := valid
			mutate(&value)
			if validateFarmPayload(&value) == nil {
				t.Fatal("invalid payload accepted")
			}
		})
	}
}

func TestFarmPersistedPayloadAndHashRoundtrip(t *testing.T) {
	raw, _ := json.Marshal(farmInput("component", "fixture-north"))
	input := FarmPayload{Action: "create", Farm: raw, InitialGrants: []FarmGrant{{SubjectUserID: "actor_a", Capabilities: []string{"agri.farm.read", "agri.farm.archive"}}}}
	if e := validateFarmPayload(&input); e != nil {
		t.Fatal(e)
	}
	payload, hash, e := farmCanonical(input)
	if e != nil {
		t.Fatal(e)
	}
	_, grantHash, e := farmCanonical(map[string]any{"initial_grants": input.InitialGrants})
	if e != nil || len(grantHash) != 64 {
		t.Fatal("grant plan hash missing", e)
	}
	op := model.AgricultureFarmOperation{ID: "fop_a", Payload: string(payload), RequestHash: hash, GrantPlanHash: grantHash}
	if _, e = farmPayload(op); e != nil {
		t.Fatal("stored payload not readable", e)
	}
	body, e := farmCommandBody(op, false)
	if e != nil {
		t.Fatal(e)
	}
	var command map[string]any
	if json.Unmarshal(body, &command) != nil || command["operation_id"] != "fop_a" || command["grant_plan_hash"] != grantHash {
		t.Fatal("command binding mismatch")
	}
	_, a, e := farmCanonical(map[string]any{"parts": []string{"tenant", "ws", "actor", "create", "one"}})
	if e != nil || len(a) != 64 {
		t.Fatal("idempotency hash missing")
	}
	_, b, e := farmCanonical(map[string]any{"parts": []string{"tenant", "ws", "actor", "create", "two"}})
	if e != nil || a == b {
		t.Fatal("idempotency collision")
	}
}

func TestFarmStrictJSONKeepsGeometryAndRejectsAliases(t *testing.T) {
	raw := []byte(`{"workspace_id":"workspace_a","action":"create","farm":{"geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}},"initial_grants":[{"subject_user_id":"actor_a","capabilities":["agri.farm.read"],"expires_at":"2027-01-01T00:00:00Z"}]}`)
	var body farmWriteBody
	if e := strictFarmJSON(raw, &body, 1<<20); e != nil {
		t.Fatal("valid RawMessage/time DTO rejected", e)
	}
	for _, bad := range []string{`{"Workspace_ID":"x"}`, `{"workspace_id":"x","workspace_id":"y"}`, `{"farm":null}`, `{"tenant_id":"x"}`} {
		if strictFarmJSON([]byte(bad), &farmWriteBody{}, 1<<20) == nil {
			t.Fatal("noncanonical/foreign field accepted")
		}
	}
	page := []byte(`{"items":[{"id":"farm_a","area_m2":"1","allowed_actions":[]}],"next_after_id":"","has_more":false}`)
	var target struct {
		Items       []map[string]any `json:"items"`
		NextAfterID string           `json:"next_after_id"`
		HasMore     bool             `json:"has_more"`
	}
	if e := strictFarmJSON(page, &target, 1<<20); e != nil {
		t.Fatal("valid bounded response rejected", e)
	}
}

func TestFarmCursorSignedScopeEpochAndType(t *testing.T) {
	s, org, _ := bindingFixture(t)
	join(t, s, org, "bob", "viewer")
	keys, _ := identityTestKeys(t, s.now(), 0x71)
	p := NewFarmPlatform(s, &GISClient{keys: keys})
	ctx := context.Background()
	position, e := p.cursorScope(ctx, "owner", org.Workspace.ID, "active", "", "")
	if e != nil {
		t.Fatal(e)
	}
	position.AfterID = "farm_0123456789abcdef0123456789abcdef"
	token, e := keys.sign(farmCursorType, position, s.now(), time.Unix(position.ExpiresAt, 0))
	if e != nil {
		t.Fatal(e)
	}
	got, e := p.cursorScope(ctx, "owner", org.Workspace.ID, "active", "", token)
	if e != nil || got.AfterID != position.AfterID {
		t.Fatal("valid cursor rejected", e)
	}
	if _, e = p.cursorScope(ctx, "bob", org.Workspace.ID, "active", "", token); !errors.Is(e, ErrInvalid) {
		t.Fatal("actor replay accepted", e)
	}
	if _, e = p.cursorScope(ctx, "owner", org.Workspace.ID, "archived", "", token); !errors.Is(e, ErrInvalid) {
		t.Fatal("filter replay accepted", e)
	}
	parts := strings.Split(token, ".")
	parts[1] = parts[1] + "A"
	if _, e = p.cursorScope(ctx, "owner", org.Workspace.ID, "active", "", strings.Join(parts, ".")); !errors.Is(e, ErrInvalid) {
		t.Fatal("tampered cursor accepted", e)
	}
	if _, e = verifyDelegation(keys, token, "GET", "/internal/agri/v1/farms/farm_a", nil, s.now()); e == nil {
		t.Fatal("cursor became delegation")
	}
	roles := []string{"analyst"}
	if e = s.ChangeMember(ctx, "owner", org.Workspace.ID, "bob", &roles, nil); e != nil {
		t.Fatal(e)
	}
	if _, e = p.cursorScope(ctx, "owner", org.Workspace.ID, "active", "", token); !errors.Is(e, ErrConflict) {
		t.Fatal("stale epoch accepted", e)
	}
	if _, e = keys.sign("arbitrary", position, s.now(), s.now().Add(time.Second)); e == nil {
		t.Fatal("unknown signing type accepted")
	}
}
func TestFarmArchiveExplicitRoleCeiling(t *testing.T) {
	for _, role := range []string{"viewer", "analyst", "editor", "field_operator", "reviewer", "tenant_admin", "billing_admin"} {
		if roleAllowsAction([]string{role}, "agri.farm.archive") {
			t.Fatalf("archive implicitly granted to %s", role)
		}
	}
	if !knownAction("agri.farm.archive") || !roleAllowsAction([]string{"farm_manager"}, "agri.farm.archive") {
		t.Fatal("explicit farm manager archive absent")
	}
}

package agriculture

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Deterministic test-only seeds. No production configuration or default uses
// these keys; deployment signing material is mandatory external configuration.
func identityTestKeys(t *testing.T, now time.Time, seedByte byte) (*ServiceKeyring, ServiceIdentityKey) {
	t.Helper()
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seedByte}, 32))
	key := ServiceIdentityKey{ID: "fixture_key", PrivateKey: private, PublicKey: private.Public().(ed25519.PublicKey), NotBefore: now.Add(-time.Hour).Truncate(time.Second), NotAfter: now.Add(time.Hour).Truncate(time.Second)}
	ring, err := NewServiceKeyring([]ServiceIdentityKey{key}, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ring, key
}

func delegationFixture(t *testing.T) (Service, AuthorizationDecision, *ServiceKeyring, ServiceIdentityKey) {
	t.Helper()
	s, org, _ := authorizationFixture(t)
	join(t, s, org, "bob", "viewer")
	if _, err := s.GrantResourceBinding(context.Background(), "owner", org.Workspace.ID, "bob", ResourceRef{"farm", "farm_a"}, []string{"agri.farm.read"}, nil, revisionPointer(0)); err != nil {
		t.Fatal(err)
	}
	decision, err := s.Authorize(context.Background(), "bob", org.Workspace.ID, "agri.farm.read", []ResourceRef{{"farm", "farm_a"}})
	if err != nil {
		t.Fatal(err)
	}
	ring, key := identityTestKeys(t, s.now(), 0x41)
	return s, decision, ring, key
}

func TestDelegationReauthorizationCanonicalBindingAndTTL(t *testing.T) {
	s, decision, keys, _ := delegationFixture(t)
	path := "/internal/agri/v1/delegation/check"
	token, canonical, err := s.issueDelegation(context.Background(), keys, "bob", decision, "POST", path, []byte(`{"z":2,"a":1}`), "operation_one")
	if err != nil || string(canonical) != `{"a":1,"z":2}` {
		t.Fatalf("issuance/canonicalization failed: %v", err)
	}
	claims, err := verifyDelegation(keys, token, "POST", path, []byte(`{ "a":1, "z":2 }`), s.now())
	if err != nil {
		t.Fatal(err)
	}
	if claims.Actor.ID != "bob" || claims.Subject != "flow-platform" || claims.Request.OperationID != "operation_one" || claims.AuthorizationVersion != decision.AuthorizationVersion || len(claims.ResourceVersions) != 1 {
		t.Fatal("claims not derived from authoritative decision")
	}
	expires, _ := identityTime(claims.ExpiresAt)
	issued, _ := identityTime(claims.IssuedAt)
	if expires.Sub(issued) > 30*time.Second || expires.After(decision.ExpiresAt) {
		t.Fatal("ticket extends authorization")
	}
	for _, tc := range []struct{ method, path, body string }{
		{"PUT", path, `{"a":1,"z":2}`}, {"POST", path + "/other", `{"a":1,"z":2}`}, {"POST", path, `{"a":2,"z":2}`},
		{"POST", path + "?q=1", `{"a":1,"z":2}`}, {"POST", "/internal/agri/v1//delegation/check", `{}`}, {"POST", "/internal/agri/v1/delegation/%63heck", `{}`},
	} {
		if _, err := verifyDelegation(keys, token, tc.method, tc.path, []byte(tc.body), s.now()); err == nil {
			t.Fatal("wrong request binding accepted")
		}
	}
	if _, err := verifyDelegation(keys, token, "POST", path, canonical, expires); err == nil {
		t.Fatal("expiry had grace period")
	}
	if _, _, err := s.issueDelegation(context.Background(), keys, "owner", decision, "POST", path, []byte(`{}`), "operation_one"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("caller substituted actor: %v", err)
	}
	if err := s.ChangeOrganizationStatus(context.Background(), "owner", decision.WorkspaceID, "disabled"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.issueDelegation(context.Background(), keys, "bob", decision, "POST", path, []byte(`{}`), "operation_one"); err == nil {
		t.Fatal("issuer ignored current membership/tenant state")
	}
}

func TestDelegationRejectsSignedInvalidClaimsAndHeaderConfusion(t *testing.T) {
	s, decision, keys, key := delegationFixture(t)
	path := "/internal/agri/v1/delegation/check"
	body := []byte(`{}`)
	token, _, err := s.issueDelegation(context.Background(), keys, "bob", decision, "POST", path, body, "operation_one")
	if err != nil {
		t.Fatal(err)
	}
	valid, err := verifyDelegation(keys, token, "POST", path, body, s.now())
	if err != nil {
		t.Fatal(err)
	}
	mutations := []func(*delegationClaims){
		func(c *delegationClaims) { c.Issuer = "agri-gis" }, func(c *delegationClaims) { c.Subject = "bob" }, func(c *delegationClaims) { c.Audience = "flow-platform" },
		func(c *delegationClaims) { c.Actor.Kind = "service" }, func(c *delegationClaims) { c.Actor.ID = "operator/owner" }, func(c *delegationClaims) { c.Scopes = []string{"agri.farm.read", "agri.farm.update"} },
		func(c *delegationClaims) { c.PolicyVersion = "unknown" }, func(c *delegationClaims) { c.AuthorizationVersion = 0 }, func(c *delegationClaims) { c.ResourceVersions = nil },
		func(c *delegationClaims) { c.FarmIDs = []string{"farm_a", "farm_a"} }, func(c *delegationClaims) { c.ResourceScope = append(c.ResourceScope, c.ResourceScope[0]) },
		func(c *delegationClaims) {
			c.IssuedAt = s.now().Add(6 * time.Second).UTC().Format("2006-01-02T15:04:05Z")
		},
		func(c *delegationClaims) {
			c.ExpiresAt = s.now().Add(60 * time.Second).UTC().Format("2006-01-02T15:04:05Z")
		},
		func(c *delegationClaims) { c.IssuedAt = strings.TrimSuffix(c.IssuedAt, "Z") + ".000Z" },
	}
	for i, mutate := range mutations {
		copyBytes, _ := json.Marshal(valid)
		var changed delegationClaims
		_ = json.Unmarshal(copyBytes, &changed)
		mutate(&changed)
		signed, err := keys.sign(delegationType, changed, s.now(), s.now().Add(30*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := verifyDelegation(keys, signed, "POST", path, body, s.now()); err == nil {
			t.Fatalf("signed invalid claims accepted: mutation %d", i)
		}
	}
	parts := strings.Split(token, ".")
	for _, header := range []string{`{"alg":"none","typ":"weavesense-agri-delegation+jws","kid":"fixture_key"}`, `{"alg":"EdDSA","typ":"weavesense-agri-service+jws","kid":"fixture_key"}`, `{"alg":"EdDSA","typ":"weavesense-agri-delegation+jws","kid":"fixture_key","jku":"https://attacker.invalid/key"}`, `{"alg":"EdDSA","alg":"EdDSA","typ":"weavesense-agri-delegation+jws","kid":"fixture_key"}`, `{"alg":"EdDSA","typ":"weavesense-agri-delegation+jws","kid":"missing"}`} {
		unsigned := base64.RawURLEncoding.EncodeToString([]byte(header)) + "." + parts[1]
		signed := unsigned + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key.PrivateKey, []byte(unsigned)))
		if _, err := verifyDelegation(keys, signed, "POST", path, body, s.now()); err == nil {
			t.Fatal("header confusion accepted")
		}
	}
	for _, bad := range []string{token + "=", token + ".extra", strings.Repeat("x", (192<<10)+1), "..."} {
		if _, err := verifyDelegation(keys, bad, "POST", path, body, s.now()); err == nil {
			t.Fatal("malformed compact JWS accepted")
		}
	}
}

func TestServiceKeyRotationRevocationAndRequestRules(t *testing.T) {
	s, decision, keys, key := delegationFixture(t)
	path := "/internal/agri/v1/delegation/check"
	token, _, err := s.issueDelegation(context.Background(), keys, "bob", decision, "GET", path, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyDelegation(keys, token, "GET", path, []byte(`{}`), s.now()); err == nil {
		t.Fatal("GET body accepted")
	}
	_, next := identityTestKeys(t, s.now(), 0x42)
	next.ID = "next_key"
	rotated, err := NewServiceKeyring([]ServiceIdentityKey{key, next}, next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyDelegation(rotated, token, "GET", path, nil, s.now()); err != nil {
		t.Fatal("valid rotation overlap rejected")
	}
	key.Revoked = true
	revoked, err := NewServiceKeyring([]ServiceIdentityKey{key, next}, next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyDelegation(revoked, token, "GET", path, nil, s.now()); err == nil {
		t.Fatal("revoked key accepted")
	}
	for _, tc := range []struct{ method, path, body, operation string }{
		{"GET", path, `{}`, ""}, {"HEAD", path, "", "operation_one"}, {"POST", path, `{}`, ""}, {"POST", path, `{"a":1,"a":2}`, "operation_one"}, {"POST", path, `{"n":9007199254740992}`, "operation_one"}, {"POST", "/internal/agri/v1/../check", `{}`, "operation_one"}, {"POST", path + "/", `{}`, "operation_one"},
	} {
		if _, _, err := bindIdentityRequest(tc.method, tc.path, []byte(tc.body), tc.operation, false); err == nil {
			t.Fatal("ambiguous request accepted")
		}
	}
}

func TestDelegationBoundedMaximumResourcesAndKeyCopy(t *testing.T) {
	s, decision, keys, key := delegationFixture(t)
	path := "/internal/agri/v1/delegation/check"
	token, _, err := s.issueDelegation(context.Background(), keys, "bob", decision, "GET", path, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := verifyDelegation(keys, token, "GET", path, nil, s.now())
	if err != nil {
		t.Fatal(err)
	}
	claims.Actor.ID = "123_numeric_platform_actor"
	claims.ResourceScope = nil
	claims.ResourceVersions = nil
	claims.FarmIDs = nil
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("farm_%03d_", i) + strings.Repeat("x", 119)
		ref := ResourceRef{"farm", id}
		claims.ResourceScope = append(claims.ResourceScope, ref)
		claims.ResourceVersions = append(claims.ResourceVersions, ResourceVersion{Resource: ref, Revision: 1})
		claims.FarmIDs = append(claims.FarmIDs, id)
	}
	large, err := keys.sign(delegationType, claims, s.now(), s.now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(large) <= 16<<10 || len(large) > 192<<10 {
		t.Fatalf("unexpected large fixture length %d", len(large))
	}
	verified, err := verifyDelegation(keys, large, "GET", path, nil, s.now())
	if err != nil || len(verified.ResourceScope) != 200 {
		t.Fatalf("maximum scope not preserved: %v", err)
	}
	claims.ResourceScope = append(claims.ResourceScope, ResourceRef{"farm", "farm_extra"})
	tooMany, err := keys.sign(delegationType, claims, s.now(), s.now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyDelegation(keys, tooMany, "GET", path, nil, s.now()); err == nil {
		t.Fatal("201 resources accepted")
	}
	// Mutating caller-owned key slices cannot alter an already configured ring.
	key.PrivateKey[0] ^= 0xff
	key.PublicKey[0] ^= 0xff
	if _, err := verifyDelegation(keys, token, "GET", path, nil, s.now()); err != nil {
		t.Fatal("keyring retained caller mutable memory")
	}
}

func TestIdentityRejectsCaseAliasesAndNonCanonicalBase64(t *testing.T) {
	s, decision, keys, key := delegationFixture(t)
	path := "/internal/agri/v1/delegation/check"
	token, _, err := s.issueDelegation(context.Background(), keys, "bob", decision, "GET", path, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	signRaw := func(header, payload []byte) string {
		unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
		return unsigned + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key.PrivateKey, []byte(unsigned)))
	}
	header, _ := base64.RawURLEncoding.DecodeString(parts[0])
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	for _, badHeader := range []string{
		`{"ALG":"EdDSA","TYP":"weavesense-agri-delegation+jws","KID":"fixture_key"}`,
		`{"alg":"EdDSA","Alg":"EdDSA","typ":"weavesense-agri-delegation+jws","kid":"fixture_key"}`,
		`{"alg":"EdDSA","typ":"weavesense-agri-delegation+jws","Kid":"fixture_key"}`,
	} {
		if _, err := verifyDelegation(keys, signRaw([]byte(badHeader), payload), "GET", path, nil, s.now()); err == nil {
			t.Fatal("case-insensitive header alias accepted")
		}
	}
	for _, pair := range [][2]string{{`"tenant_id":`, `"Tenant_ID":`}, {`"actor":`, `"Actor":`}, {`"kind":`, `"Kind":`}, {`"revision":`, `"Revision":`}, {`"method":`, `"METHOD":`}} {
		bad := strings.Replace(string(payload), pair[0], pair[1], 1)
		if bad == string(payload) {
			t.Fatalf("missing test field %s", pair[0])
		}
		if _, err := verifyDelegation(keys, signRaw(header, []byte(bad)), "GET", path, nil, s.now()); err == nil {
			t.Fatalf("signed case alias accepted: %s", pair[1])
		}
	}
	for segment := 0; segment < 3; segment++ {
		for _, whitespace := range []string{"\r", "\n", "\r\n", " "} {
			changed := append([]string(nil), parts...)
			changed[segment] = changed[segment][:2] + whitespace + changed[segment][2:]
			if _, err := verifyDelegation(keys, strings.Join(changed, "."), "GET", path, nil, s.now()); err == nil {
				t.Fatalf("whitespace in compact segment %d accepted", segment)
			}
		}
	}
	var config serviceIdentityConfig
	for _, raw := range []string{`{"GIS_BASE_URL":"https://gis.example"}`, `{"gis_base_url":"https://gis.example","GIS_BASE_URL":"https://evil.example"}`, `{"platform_keys":[{"KID":"example"}]}`, `{"platform_keys":[{"revoked":null}]}`} {
		if strictIdentityJSON([]byte(raw), &config, 4096) == nil {
			t.Fatal("case alias/null in identity configuration accepted")
		}
	}
	var callback authorityCheckBody
	if strictIdentityJSON([]byte(`{"actor_user_id":"bob","Tenant_ID":"tenant","workspace_id":"workspace","authorization_version":1}`), &callback, 4096) == nil {
		t.Fatal("callback identity alias accepted")
	}
}

package flow

import (
	"testing"
	"time"
)

// Token produced by services/loom-engine/flow/auth.ts sign() at iat=1800000000.
const nodeToken = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJsb29tLWVuZ2luZSIsImF1ZCI6ImZsb3ctcGxhdGZvcm0iLCJ0ZW5hbnRfaWQiOiJ0bl9hIiwid29ya3NwYWNlX2lkIjoid3NfYSIsImFjdG9yIjp7InR5cGUiOiJzZXJ2aWNlX3ByaW5jaXBhbCIsImlkIjoibG9vbS1lbmdpbmUifSwic2NvcGVzIjpbImZsb3cuZXZlbnRzLndyaXRlIl0sInBvbGljeV92ZXJzaW9uIjoicG9saWN5X3YxIiwiaWF0IjoxODAwMDAwMDAwLCJleHAiOjE4MDAwMDAzMDAsImp0aSI6Imp0aV8yYTRlMDU3OGI5MmZjYjljNDlhMjMxOGMifQ.aquYQPPimmboSnQbmgch7OxNhnC5Clet1D5tVb-wJnk"

func TestVerifiesLoomEngineSignedToken(t *testing.T) {
	c, err := Verify(key, nodeToken, "flow-platform", time.Unix(1_800_000_010, 0))
	if err != nil || c.TenantID != "tn_a" || c.Actor.Type != "service_principal" {
		t.Fatalf("interop: %v %+v", err, c)
	}
}

package agriculture

import (
	"testing"
	"time"
)

func TestResourceAccessEmptyFarmNeverImplicitGrant(t *testing.T) {
	ref := ResourceRef{"asset", "asset_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	until := time.Now().Add(time.Minute)
	for _, farms := range [][]string{nil, {}} {
		proof := ResourceOwnership{Resource: ref, FarmIDs: farms}
		for _, bindings := range []map[ResourceRef]effectiveGrant{
			{},
			{{"farm", "unrelated"}: {Capabilities: []string{"agri.asset.read", "agri.authorization.update"}}},
		} {
			visible, allowed, _ := resourceAccess(ref, proof, bindings, "agri.asset.read", until)
			if visible || allowed {
				t.Fatal("empty farm set inherited an implicit grant")
			}
		}
		bindings := map[ResourceRef]effectiveGrant{ref: {Capabilities: []string{"agri.asset.read"}}}
		visible, allowed, _ := resourceAccess(ref, proof, bindings, "agri.asset.read", until)
		if !visible || !allowed {
			t.Fatal("explicit effective direct grant was lost")
		}
		_, delegated, _ := resourceAccess(ref, proof, bindings, "agri.authorization.update", until)
		if delegated {
			t.Fatal("business direct grant became governance authority")
		}
	}
}

func TestResourceAccessNonemptyExplicitParentUnchanged(t *testing.T) {
	ref := ResourceRef{"asset", "asset_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	proof := ResourceOwnership{Resource: ref, FarmIDs: []string{"farm_a"}}
	until := time.Now().Add(time.Minute)
	for _, cap := range []string{"agri.farm.read", "agri.asset.read"} {
		bindings := map[ResourceRef]effectiveGrant{{"farm", "farm_a"}: {Capabilities: []string{cap}}}
		visible, allowed, _ := resourceAccess(ref, proof, bindings, "agri.asset.read", until)
		if !visible || allowed != (cap == "agri.asset.read") {
			t.Fatal("explicit parent capability semantics changed")
		}
	}
}

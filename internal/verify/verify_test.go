package verify

import "testing"

func TestAllowUnsignedIndex_CanonicalizesEntries(t *testing.T) {
	idx := allowUnsignedIndex([]string{
		"cilium/hubble-ui",
		"quay.io/cilium/hubble-relay",
	})
	// "cilium/hubble-ui" should canonicalize the same way as a real
	// pull ref of the same image, so e.g. "cilium/hubble-ui:v0.13.2"
	// hits the lookup.
	if !idx[canonicalRepo("cilium/hubble-ui:v0.13.2")] {
		t.Errorf("idx missing canonical match for cilium/hubble-ui:v0.13.2; entries: %v", idx)
	}
	if !idx[canonicalRepo("quay.io/cilium/hubble-relay:v0.13.2")] {
		t.Errorf("idx missing canonical match for quay.io/cilium/hubble-relay")
	}
	if idx[canonicalRepo("cilium/other:1")] {
		t.Errorf("idx false positive for unrelated repo")
	}
}

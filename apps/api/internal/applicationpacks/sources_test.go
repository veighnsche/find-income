package applicationpacks

import "testing"

func TestCorrectionRequiresSameApprovedSourceSnapshots(t *testing.T) {
	prior := fixture(t).Sources
	current := fixture(t).Sources
	if !SameApprovedSourceSnapshots(prior, current) {
		t.Fatal("identical approved career snapshots rejected")
	}
	changed := append([]Source(nil), current...)
	changed[1].SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if SameApprovedSourceSnapshots(prior, changed) {
		t.Fatal("changed career source digest accepted")
	}
	changed = append([]Source(nil), current...)
	changed[1].ID = changed[0].ID
	if SameApprovedSourceSnapshots(prior, changed) {
		t.Fatal("duplicate current source identity accepted")
	}
}

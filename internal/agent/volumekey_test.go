package agent

import "testing"

func TestOnlyAVolumeIDIsAcceptedForAKeyPath(t *testing.T) {
	for _, id := range []string{
		"vol-xjqtss6wzsxza", "vol-a", "vol-0123456789",
	} {
		if !safeVolumeID(id) {
			t.Fatalf("%q was refused, so its key can never be placed and the disk is "+
				"silently left out", id)
		}
	}

	for _, id := range []string{
		"", "vol", "vol-", "i-7dn7y7vsn7218", "vol-../escape", "vol-UPPER",
		"vol-with.dot", "vol-with/slash",
		"vol-0123456789012345678901234567890123456789",
	} {
		if safeVolumeID(id) {
			t.Fatalf("%q was accepted", id)
		}
	}
}

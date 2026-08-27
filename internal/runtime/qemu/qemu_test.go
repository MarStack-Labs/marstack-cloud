package qemu

import "testing"

func TestADeviceIDFollowsTheVolumeNotItsPositionInTheList(t *testing.T) {
	first := diskDeviceID("vol-xr6yrzzt4paf8")
	second := diskDeviceID("vol-8r64deaqyh27j")

	if first == second {
		t.Fatal("two volumes share a device id")
	}
	if first != diskDeviceID("vol-xr6yrzzt4paf8") {
		t.Fatal("the same volume produced two device ids")
	}

	for _, id := range []string{first, second} {
		if len(id) > 20 {
			t.Fatalf("%q is too long for a qdev id", id)
		}
		for _, char := range id {
			ok := (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9')
			if !ok {
				t.Fatalf("%q holds %q, which qemu will not take as an id", id, char)
			}
		}
	}
}

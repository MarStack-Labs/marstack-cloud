package qemu

import "strings"

const DefaultRoot = "/var/lib/marstack"

func diskDeviceID(volumeID string) string {
	suffix := volumeID
	if at := strings.IndexByte(suffix, '-'); at >= 0 {
		suffix = suffix[at+1:]
	}
	if len(suffix) > 12 {
		suffix = suffix[:12]
	}
	return "vol" + suffix
}

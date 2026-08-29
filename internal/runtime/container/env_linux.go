package container

import (
	"sort"
	"strings"
)

func mergeEnv(image []string, wanted map[string]string) []string {
	if len(wanted) == 0 {
		return image
	}

	merged := make([]string, 0, len(image)+len(wanted))
	for _, entry := range image {
		name, _, found := strings.Cut(entry, "=")
		if found {
			if _, overridden := wanted[name]; overridden {
				continue
			}
		}
		merged = append(merged, entry)
	}

	names := make([]string, 0, len(wanted))
	for name := range wanted {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		merged = append(merged, name+"="+wanted[name])
	}
	return merged
}

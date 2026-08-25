package validate

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
)

var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func Name(field, value string) error {
	if value == "" {
		return fault.Invalid("invalid_"+field, fmt.Sprintf("%s must not be empty", field))
	}
	if !namePattern.MatchString(value) {
		return fault.Invalid("invalid_"+field, fmt.Sprintf(
			"%s must be 1-63 characters of lowercase letters, digits, or hyphens, starting and ending with a letter or digit",
			field,
		))
	}
	return nil
}

func OneOf(field, value string, allowed ...string) error {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fault.Invalid("invalid_"+field, fmt.Sprintf(
		"%s must be one of: %s", field, strings.Join(allowed, ", "),
	))
}

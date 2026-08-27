package interval

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func Parse(text string) (time.Duration, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, nil
	}

	multiplier := time.Duration(0)
	switch {
	case strings.HasSuffix(text, "d"):
		multiplier = 24 * time.Hour
	case strings.HasSuffix(text, "w"):
		multiplier = 7 * 24 * time.Hour
	}

	if multiplier > 0 {
		count, err := strconv.Atoi(strings.TrimSpace(text[:len(text)-1]))
		if err != nil {
			return 0, fmt.Errorf("%q is not a number of days or weeks", text)
		}
		return time.Duration(count) * multiplier, nil
	}

	parsed, err := time.ParseDuration(text)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration such as 12h, 30d or 4w", text)
	}
	return parsed, nil
}

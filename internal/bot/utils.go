// internal/bot/utils.go

package bot

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// parseDuration parses duration strings like "30", "30m", "1h30m", "2h"
func parseDuration(input string) (time.Duration, error) {
	if input == "" {
		return 0, fmt.Errorf("empty duration")
	}

	// Try to parse as simple number (minutes)
	if minutes, err := strconv.ParseInt(input, 10, 64); err == nil {
		return time.Duration(minutes) * time.Minute, nil
	}

	// Try to parse with units
	patterns := []struct {
		regex *regexp.Regexp
		unit  time.Duration
	}{
		{regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*h$`), time.Hour},
		{regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*min?$`), time.Minute},
		{regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*m$`), time.Minute},
		{regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*s$`), time.Second},
	}

	for _, p := range patterns {
		if matches := p.regex.FindStringSubmatch(input); len(matches) > 1 {
			if val, err := strconv.ParseFloat(matches[1], 64); err == nil {
				return time.Duration(val * float64(p.unit)), nil
			}
		}
	}

	// Try Go's native duration parser as fallback
	if duration, err := time.ParseDuration(input); err == nil {
		return duration, nil
	}

	return 0, fmt.Errorf("invalid duration format: %s", input)
}

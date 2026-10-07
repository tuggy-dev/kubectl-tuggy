package v1alpha1

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Duration is a time.Duration written as a string such as "90m", "8h" or
// "3d". It adds a day unit ("d" = 24h) to Go's duration syntax because TTLs
// are usually measured in hours or days.
type Duration struct {
	time.Duration
}

var daysPrefix = regexp.MustCompile(`^(\d+)d(.*)$`)

// ParseDuration parses a duration such as "90m", "8h", "3d" or "1d12h".
// "0" means zero.
func ParseDuration(s string) (Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Duration{}, fmt.Errorf("empty duration")
	}

	var days time.Duration
	if m := daysPrefix.FindStringSubmatch(s); m != nil {
		n, err := strconv.ParseInt(m[1], 10, 32)
		if err != nil {
			return Duration{}, fmt.Errorf("invalid duration %q", s)
		}
		days = time.Duration(n) * 24 * time.Hour
		s = m[2]
		if s == "" {
			return Duration{Duration: days}, nil
		}
	}

	d, err := time.ParseDuration(s)
	if err != nil {
		return Duration{}, fmt.Errorf("invalid duration %q: use a number with a unit, for example 90m, 8h or 3d", s)
	}
	return Duration{Duration: days + d}, nil
}

// String formats the duration compactly, for example "3d", "1d12h" or "90m".
func (d Duration) String() string {
	if d.Duration == 0 {
		return "0"
	}
	if d.Duration < 0 {
		return d.Duration.String()
	}

	var b strings.Builder
	rest := d.Duration
	if days := rest / (24 * time.Hour); days > 0 {
		fmt.Fprintf(&b, "%dd", days)
		rest -= days * 24 * time.Hour
	}
	if h := rest / time.Hour; h > 0 {
		fmt.Fprintf(&b, "%dh", h)
		rest -= h * time.Hour
	}
	if m := rest / time.Minute; m > 0 {
		fmt.Fprintf(&b, "%dm", m)
		rest -= m * time.Minute
	}
	if rest > 0 {
		b.WriteString(rest.String())
	}
	return b.String()
}

// MarshalJSON writes the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// UnmarshalJSON reads a duration string. A bare 0 is also accepted, because
// YAML turns "ttl: 0" into a number.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		var n float64
		if json.Unmarshal(b, &n) == nil && n == 0 {
			*d = Duration{}
			return nil
		}
		return fmt.Errorf("duration needs a unit, for example \"8h\" or \"3d\"")
	}
	parsed, err := ParseDuration(s)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

package rules

import (
	"testing"
	"time"
)

func TestParseAge(t *testing.T) {
	good := map[string]time.Duration{
		"90m": 90 * time.Minute,
		"12h": 12 * time.Hour,
		"3d":  72 * time.Hour,
		"2w":  14 * 24 * time.Hour,
		"0d":  0,
	}
	for in, want := range good {
		got, err := ParseAge(in)
		if err != nil || got != want {
			t.Errorf("ParseAge(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "d", "3", "3y", "-1d", "1.5d"} {
		if _, err := ParseAge(in); err == nil {
			t.Errorf("ParseAge(%q) should fail", in)
		}
	}
}

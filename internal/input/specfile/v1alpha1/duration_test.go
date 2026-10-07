package v1alpha1

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: "0", want: 0},
		{in: "90m", want: 90 * time.Minute},
		{in: "8h", want: 8 * time.Hour},
		{in: "3d", want: 72 * time.Hour},
		{in: "1d12h", want: 36 * time.Hour},
		{in: " 2h ", want: 2 * time.Hour},
		{in: "", wantErr: true},
		{in: "8", wantErr: true},
		{in: "eight hours", wantErr: true},
		{in: "3dd", wantErr: true},
		{in: "99999999999d", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseDuration(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseDuration(%q) = %v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDuration(%q) error: %v", tt.in, err)
			}
			if got.Duration != tt.want {
				t.Errorf("ParseDuration(%q) = %v, want %v", tt.in, got.Duration, tt.want)
			}
		})
	}
}

func TestDurationString(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "0"},
		{90 * time.Minute, "1h30m"},
		{8 * time.Hour, "8h"},
		{72 * time.Hour, "3d"},
		{36 * time.Hour, "1d12h"},
		{45 * time.Second, "45s"},
	}
	for _, tt := range tests {
		if got := (Duration{tt.in}).String(); got != tt.want {
			t.Errorf("Duration(%v).String() = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDurationJSONRoundTrip(t *testing.T) {
	for _, s := range []string{"8h", "3d", "1d12h", "90m"} {
		d, err := ParseDuration(s)
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		var back Duration
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		if back != d {
			t.Errorf("round trip of %q: got %v, want %v", s, back, d)
		}
	}
}

func TestDurationUnmarshalNumbers(t *testing.T) {
	var d Duration
	if err := json.Unmarshal([]byte("0"), &d); err != nil || d.Duration != 0 {
		t.Errorf("bare 0: got %v, %v; want 0, nil", d, err)
	}
	if err := json.Unmarshal([]byte("8"), &d); err == nil {
		t.Error("bare 8 without a unit should be an error")
	}
}

package feed

import "testing"

func TestParseTime(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int64
	}{
		{"feed timestamp with microseconds", "2026-09-24T09:25:13.543924+00:00", 1790241913543924},
		{"milliseconds Z", "2026-09-24T10:04:12.345Z", 1790244252345000},
		{"no fraction Z", "2026-09-24T10:00:00Z", 1790244000000000},
		{"nanoseconds truncated", "2026-09-24T10:04:12.123456789Z", 1790244252123456},
		{"offset applied", "2026-09-24T13:04:12.345+03:00", 1790244252345000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTime(tt.input)
			if err != nil {
				t.Fatalf("ParseTime(%q) returned error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ParseTime(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseTimeRejects(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"space instead of T", "2026-09-24 10:00:00"},
		{"garbage", "garbage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseTime(tt.input); err == nil {
				t.Errorf("ParseTime(%q) returned nil error, want error", tt.input)
			}
		})
	}
}

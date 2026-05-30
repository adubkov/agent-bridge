package main

import (
	"testing"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{
			name: "shorter than max",
			in:   "hello",
			max:  10,
			want: "hello",
		},
		{
			name: "equal to max",
			in:   "hello",
			max:  5,
			want: "hello",
		},
		{
			name: "longer ascii",
			in:   "hello world",
			max:  5,
			want: "hello\n…(truncated, 11 bytes total)",
		},
		{
			name: "utf-8 safe cut rocket emoji 1",
			in:   "hello 🚀", // rocket is 4 bytes: \xf0\x9f\x9a\x80
			max:  9,        // cut is in the middle of rocket (index 6, 7, 8, 9)
			want: "hello \n…(truncated, 10 bytes total)",
		},
		{
			name: "utf-8 safe cut rocket emoji 2",
			in:   "hello 🚀",
			max:  8,
			want: "hello \n…(truncated, 10 bytes total)",
		},
		{
			name: "utf-8 safe cut rocket emoji 3",
			in:   "hello 🚀",
			max:  7,
			want: "hello \n…(truncated, 10 bytes total)",
		},
		{
			name: "utf-8 safe cut rocket emoji 4",
			in:   "hello 🚀",
			max:  6,
			want: "hello \n…(truncated, 10 bytes total)",
		},
		{
			name: "utf-8 safe cut rocket emoji 5",
			in:   "hello 🚀",
			max:  5,
			want: "hello\n…(truncated, 10 bytes total)",
		},
		{
			name: "empty string",
			in:   "",
			max:  5,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.in, tt.max)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q; want %q", tt.in, tt.max, got, tt.want)
			}
		})
	}
}

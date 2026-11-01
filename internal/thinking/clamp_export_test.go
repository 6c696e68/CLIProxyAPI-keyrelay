package thinking

import "testing"

func TestClampLevelToSupported(t *testing.T) {
	cases := []struct {
		name      string
		level     string
		supported []string
		want      string
		wantOK    bool
	}{
		{
			name:      "clamps minimal down to the lowest supported",
			level:     "minimal",
			supported: []string{"low", "medium", "high", "xhigh", "max"},
			want:      "low",
			wantOK:    true,
		},
		{
			name:      "clamps max down to high",
			level:     "max",
			supported: []string{"low", "medium", "high"},
			want:      "high",
			wantOK:    true,
		},
		{
			name:      "tie prefers the lower level",
			level:     "medium",
			supported: []string{"low", "high"},
			want:      "low",
			wantOK:    true,
		},
		{
			name:      "rejects ultra",
			level:     "ultra",
			supported: []string{"low", "medium", "high"},
			wantOK:    false,
		},
		{
			name:      "rejects auto",
			level:     "auto",
			supported: []string{"low", "medium", "high"},
			wantOK:    false,
		},
		{
			name:      "rejects bogus",
			level:     "bogus",
			supported: []string{"low", "medium", "high"},
			wantOK:    false,
		},
		{
			name:      "rejects an empty supported list",
			level:     "high",
			supported: nil,
			wantOK:    false,
		},
		{
			name:      "keeps an already supported level",
			level:     "high",
			supported: []string{"low", "medium", "high"},
			want:      "high",
			wantOK:    true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ClampLevelToSupported(tc.level, tc.supported)
			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("ClampLevelToSupported(%q, %v) = %q, %v; want %q, %v", tc.level, tc.supported, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

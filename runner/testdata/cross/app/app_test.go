package app

import "testing"

func TestPercent(t *testing.T) {
	for name, tc := range map[string]struct{ x, want int }{
		"below": {-5, 0},
		"above": {150, 100},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Percent(tc.x); got != tc.want {
				t.Errorf("Percent(%d) = %d, want %d", tc.x, got, tc.want)
			}
		})
	}
}

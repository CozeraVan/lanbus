package worker

import "testing"

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		p, n string
		ok   bool
	}{
		{"shot.png", "shot.png", true},
		{"dist/**/*.png", "dist/icon.png", true},
		{"dist/**/*.png", "dist/icons/a.png", true},
		{"dist/**/*.png", "other/icon.png", false},
		{"screenshot*.png", "screenshot-1.png", true},
	}
	for _, c := range cases {
		if got := matchGlob(c.p, c.n); got != c.ok {
			t.Fatalf("matchGlob(%q,%q)=%v want %v", c.p, c.n, got, c.ok)
		}
	}
}

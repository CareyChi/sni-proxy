package update

import "testing"

func TestCompareVersions(t *testing.T) {
	for _, test := range []struct {
		left, right string
		want        int
	}{
		{"v1.2.0", "v1.1.9", 1},
		{"v1.0.0", "1.0", 0},
		{"v1.0.0", "v1.0.1", -1},
	} {
		if got := compareVersions(test.left, test.right); got != test.want {
			t.Fatalf("compareVersions(%q, %q) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}

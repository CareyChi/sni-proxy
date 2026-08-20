package update

import "testing"

func TestCompareVersions(t *testing.T) {
	for _, test := range []struct {
		left, right string
		want        int
	}{
		{"v1.2.0", "v1.1.9", 1},
		{"v1.0.0", "1.0.0", 0},
		{"v1.0.0", "v1.0.1", -1},
		{"1.0.0-beta", "1.0.0", -1},
		{"1.0.0-beta.2", "1.0.0-beta.10", -1},
		{"1.0.0+build.2", "1.0.0+build.1", 0},
	} {
		got, err := compareVersions(test.left, test.right)
		if err != nil {
			t.Fatalf("compareVersions(%q, %q): %v", test.left, test.right, err)
		}
		if got != test.want {
			t.Fatalf("compareVersions(%q, %q) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}

func TestInvalidVersions(t *testing.T) {
	for _, value := range []string{"1.0", "1.0.x", "1.01.0", "1.0.0-beta.01", "1.0.0+", "v"} {
		if _, err := parseSemVersion(value); err == nil {
			t.Fatalf("parseSemVersion(%q) succeeded", value)
		}
	}
}

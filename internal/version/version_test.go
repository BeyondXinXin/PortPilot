package version

import "testing"

func TestDisplay(t *testing.T) {
	previous := Version
	t.Cleanup(func() { Version = previous })

	for _, test := range []struct {
		version string
		want    string
	}{
		{"", "dev"},
		{"dev", "dev"},
		{"dev-abc1234", "dev-abc1234"},
		{"v1.2.3", "v1.2.3"},
		{"1.2.3", "v1.2.3"},
	} {
		Version = test.version
		if got := Display(); got != test.want {
			t.Errorf("Display() with Version %q = %q, want %q", test.version, got, test.want)
		}
	}
}

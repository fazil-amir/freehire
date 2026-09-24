package classify

import "testing"

func TestParseCatalogueTechOnly(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want bool
	}{
		{"", true},
		{"true", true},
		{"1", true},
		{"false", false},
		{"0", false},
		// A typo keeps today's catalogue rather than silently opening it.
		{"flase", true},
	} {
		if got := parseCatalogueTechOnly(tt.in); got != tt.want {
			t.Errorf("parseCatalogueTechOnly(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

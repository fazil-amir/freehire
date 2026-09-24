package main

import "testing"

// The rule must match classify.parseCatalogueTechOnly, or the badge would
// state a mode the crawls are not in.
func TestParseTechOnly(t *testing.T) {
	cases := map[string]bool{
		"":      true, // unset: IT only
		"true":  true,
		"1":     true,
		"false": false,
		"0":     false,
		"FALSE": false,
		"nope":  true, // unreadable: IT only, like the binaries
	}
	for in, want := range cases {
		if got := parseTechOnly(in); got != want {
			t.Errorf("parseTechOnly(%q) = %v, want %v", in, got, want)
		}
	}
}

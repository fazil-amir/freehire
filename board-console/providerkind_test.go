package main

import "testing"

func TestDisplayName(t *testing.T) {
	cases := map[string]string{
		"remoteok":           "Remoteok",
		"gulftalent":         "Gulftalent",
		"cryptocurrencyjobs": "Cryptocurrencyjobs",
		"whatjobs-uk":        "Whatjobs Uk",
		"habr_career":        "Habr Career",
	}
	for in, want := range cases {
		if got := displayName(in); got != want {
			t.Errorf("displayName(%q) = %q, want %q", in, got, want)
		}
	}
}

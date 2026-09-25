package outboundurl

import "testing"

func TestTag(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no query string appends with ?",
			in:   "https://ats.example.com/job/123",
			want: "https://ats.example.com/job/123?utm_source=nxtchap.ai",
		},
		{
			name: "existing query appends with &",
			in:   "https://ats.example.com/job?id=123",
			want: "https://ats.example.com/job?id=123&utm_source=nxtchap.ai",
		},
		{
			name: "existing utm_source is overwritten",
			in:   "https://ats.example.com/job?utm_source=indeed",
			want: "https://ats.example.com/job?utm_source=nxtchap.ai",
		},
		{
			name: "fragment is preserved after the query",
			in:   "https://ats.example.com/job#apply",
			want: "https://ats.example.com/job?utm_source=nxtchap.ai#apply",
		},
		{
			name: "empty url is returned unchanged",
			in:   "",
			want: "",
		},
		{
			name: "unparseable url is returned unchanged",
			in:   "https://ats.example.com/%zz",
			want: "https://ats.example.com/%zz",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Tag(tt.in); got != tt.want {
				t.Errorf("Tag(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestUntag(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"our tag is removed", "https://ats.example.com/job?id=7&utm_source=nxtchap.ai", "https://ats.example.com/job?id=7"},
		// Stamped before the switch: a search document indexed then carries it until
		// the next full reindex, so it is still ours to remove.
		{"the legacy freehire.me tag is removed too", "https://ats.example.com/job?utm_source=freehire.me", "https://ats.example.com/job"},
		{"a source's own utm_source stays", "https://ats.example.com/job?utm_source=linkedin", "https://ats.example.com/job?utm_source=linkedin"},
		{"no tag, unchanged", "https://ats.example.com/job/123", "https://ats.example.com/job/123"},
		{"round trip", Tag("https://ats.example.com/job?id=9"), "https://ats.example.com/job?id=9"},
		{"empty url is returned unchanged", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Untag(tt.in); got != tt.want {
				t.Errorf("Untag(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

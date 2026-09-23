package main

import "time"

// buildID and buildRaw are stamped by the Dockerfile at image build time
// (-ldflags -X). The build context excludes .git, so there is no commit to
// stamp: buildID is a fingerprint of board-console's own source files —
// the same code always yields the same ID, and any change yields a new one.
// A plain `go build` leaves the defaults.
var (
	buildID  = "dev"
	buildRaw = "" // RFC 3339, UTC
)

// buildDate is when the image was built; the zero time when unstamped, which
// the footer simply leaves out.
func buildDate() time.Time {
	t, err := time.Parse(time.RFC3339, buildRaw)
	if err != nil {
		return time.Time{}
	}
	return t
}

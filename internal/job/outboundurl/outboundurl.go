// Package outboundurl decorates the outbound job-posting URLs freehire serves so
// the destination ATS/board can attribute the click back to us. It touches only
// the served (public) representation — the raw jobs.url column stays untagged, so
// dedup, content-hashing, and liveness probing keep working on the canonical URL.
package outboundurl

import "net/url"

// utmSource is the fixed utm_source value stamped on every outbound link, mirroring
// how the notification builders hardcode "telegram-bot"/"email" for internal links.
// It is the brand the destination ATS attributes the click to.
const utmSource = "nxtchap.ai"

// legacyUTMSources are values this package stamped before, which Untag still removes.
// A served URL is not only built fresh: the Meilisearch index stores jobview output,
// tag included, and an incremental push never rewrites it (the URL is not part of
// content_hash), so until a full reindex search results still carry the old value —
// and an agent-facing consumer that untags them must not pass it through.
var legacyUTMSources = []string{"freehire.me"}

// Untag removes the utm_source this package adds, returning the URL as the source
// published it. It exists because the tag is applied on the way OUT of jobview, so a
// consumer holding a served Job has no other route back to the canonical URL — and some
// consumers must have it. The OJCP projection is the first: that standard's
// `official_job_url` is defined as the canonical page on the employer's own site and is
// what agents deduplicate and domain-verify against, so a tracking parameter there breaks
// both.
//
// Only OUR parameter is removed. A utm_source the source itself published is already
// overwritten by Tag, so it cannot be recovered and is not pretended to be; every other
// query parameter is left exactly as it was, since it may be the posting's identifier.
func Untag(tagged string) string {
	if tagged == "" {
		return tagged
	}
	u, err := url.Parse(tagged)
	if err != nil {
		return tagged
	}
	q := u.Query()
	if !isOurs(q.Get("utm_source")) {
		return tagged
	}
	q.Del("utm_source")
	u.RawQuery = q.Encode()
	return u.String()
}

// isOurs reports whether a utm_source value is one this package stamps, now or before.
func isOurs(v string) bool {
	if v == utmSource {
		return true
	}
	for _, old := range legacyUTMSources {
		if v == old {
			return true
		}
	}
	return false
}

// Tag returns raw with utm_source=nxtchap.ai set as a query parameter. It parses
// the URL so an existing query string is preserved (the tag is appended with the
// correct "?"/"&" separator) and any pre-existing utm_source is overwritten, keeping
// attribution consistently ours. An empty or unparseable URL is returned unchanged
// rather than mangled.
func Tag(raw string) string {
	if raw == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("utm_source", utmSource)
	u.RawQuery = q.Encode()
	return u.String()
}

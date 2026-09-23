package main

import (
	"encoding/json"
	"net/http"
	"net/url"
)

// fetchHeader marks a request made by app.js's fetch() rather than a plain
// form submission. Every POST action answers the two differently: a fetch
// gets a status code and stays on its page, a plain form post (JS
// unavailable) gets a redirect back to the page it came from.
const fetchHeader = "X-Board-Console-Fetch"

func isFetch(r *http.Request) bool { return r.Header.Get(fetchHeader) == "1" }

// actionDone finishes a POST action. It never sends the operator anywhere
// they didn't come from — in particular not to Activity, which stays a
// page you visit, never one you are pushed to.
func actionDone(w http.ResponseWriter, r *http.Request, fallback string) {
	if isFetch(r) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, backTo(r, fallback), http.StatusSeeOther)
}

// actionError reports a validation failure to a fetch caller as JSON the
// modal can show inline.
func actionError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// backTo is the same-origin page the form was submitted from, or fallback
// when the Referer is missing or points elsewhere.
func backTo(r *http.Request, fallback string) string {
	ref, err := url.Parse(r.Referer())
	if err != nil || ref.Host != r.Host || ref.Path == "" {
		return fallback
	}
	return ref.RequestURI()
}

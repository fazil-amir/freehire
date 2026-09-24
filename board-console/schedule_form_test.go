package main

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestFormTimes_ConvertsLocalRowsToUTC(t *testing.T) {
	form := url.Values{
		"hour":      {"6", "0", "", "23"},
		"minute":    {"0", "15", "30", "45"},
		"tz_offset": {"330"}, // IST
	}
	r := httptest.NewRequest("POST", "/schedules/save", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	got, err := formTimes(r)
	if err != nil {
		t.Fatal(err)
	}
	// 06:00 IST = 00:30 UTC; 00:15 IST = 18:45 UTC the day before; the
	// unfinished row is skipped; 23:45 IST = 18:15 UTC.
	if want := []int{30, 18*60 + 45, 18*60 + 15}; !equalInts(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	bad := url.Values{"hour": {"6"}, "minute": {"10"}}
	r = httptest.NewRequest("POST", "/schedules/save", strings.NewReader(bad.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = r.ParseForm()
	if _, err := formTimes(r); err == nil {
		t.Error("a minute off the 15-minute grid must be refused")
	}
}

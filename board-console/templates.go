package main

import (
	"embed"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Templates renders every page through layout.html, which each page
// template supplies its body to via the "content" block.
type Templates struct {
	tmpl *template.Template
}

func LoadTemplates() *Templates {
	t, err := parseTemplates()
	if err != nil {
		log.Fatalf("parse templates: %v", err)
	}
	return t
}

// parseTemplates is LoadTemplates without the exit, so a test can prove
// every template parses and renders.
func parseTemplates() (*Templates, error) {
	funcs := template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		// iso is a <time datetime> value: the server runs in UTC, so app.js
		// re-renders every <time data-local> in the viewer's own timezone.
		"iso":             func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
		"buildID":         func() string { return buildID },
		"techOnly":        catalogueTechOnly,
		"explainSections": explainSections,
		"buildDate": func() *time.Time {
			if t := buildDate(); !t.IsZero() {
				return &t
			}
			return nil
		},
	}
	t, err := template.New("").Funcs(funcs).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Templates{tmpl: t}, nil
}

func (t *Templates) Render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

// Fragment renders one named template to a string — for a JSON endpoint
// that returns a piece of the page (see handleExplain).
func (t *Templates) Fragment(name string, data any) (string, error) {
	var b strings.Builder
	if err := t.tmpl.ExecuteTemplate(&b, name, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

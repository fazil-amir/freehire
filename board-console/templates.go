package main

import (
	"embed"
	"html/template"
	"log"
	"net/http"
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
	funcs := template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		// iso is a <time datetime> value: the server runs in UTC, so app.js
		// re-renders every <time data-local> in the viewer's own timezone.
		"iso":     func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
		"buildID": func() string { return buildID },
		"buildDate": func() *time.Time {
			if t := buildDate(); !t.IsZero() {
				return &t
			}
			return nil
		},
	}
	t, err := template.New("").Funcs(funcs).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		log.Fatalf("parse templates: %v", err)
	}
	return &Templates{tmpl: t}
}

func (t *Templates) Render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

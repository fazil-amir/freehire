// Command bulk-add-boards is cmd/add-board's exact insertion path (boardcatalog.Validate +
// boardcatalog.NewInserter(...).Insert(..., StatusActive)), driven from a CSV instead of one
// flag set per invocation — built for onboarding a large combined board list (e.g. a
// pre-migration sources/*.yml export merged with a third-party ATS-company inventory) without
// spawning `go run ./cmd/add-board` once per row, which does not scale past a few hundred rows.
//
// Input: a CSV with header `provider,board,company` (extra columns are ignored, looked up by
// name — order doesn't matter). Same dry-run-by-default convention as add-board: reports what
// it would add and writes nothing until --apply is passed.
//
// A row that fails validation (unknown provider, missing board for a non-boardless provider) or
// that already exists (ErrDuplicateBoard — already pending/active) is skipped and counted, never
// fatal to the run, since a 150k+ row combined CSV is expected to contain some of both.
//
// Usage:
//
//	go run ./cmd/bulk-add-boards -in combined_boards.csv                 # dry run, reports only
//	go run ./cmd/bulk-add-boards -in combined_boards.csv --apply         # actually inserts
//	go run ./cmd/bulk-add-boards -in combined_boards.csv --apply -provider=greenhouse  # one provider only
package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/strelov1/freehire/internal/ingest/boardcatalog"
	"github.com/strelov1/freehire/internal/ingest/sources"
	"github.com/strelov1/freehire/internal/platform/db"
	"github.com/strelov1/freehire/internal/platform/worker"
)

type row struct {
	Provider string
	Board    string
	Company  string
}

var requiredColumns = []string{"provider", "board", "company"}

func parseCSV(r io.Reader) ([]row, error) {
	cr := csv.NewReader(r)
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	col := make(map[string]int, len(header))
	for i, name := range header {
		col[name] = i
	}
	for _, name := range requiredColumns {
		if _, ok := col[name]; !ok {
			return nil, fmt.Errorf("missing required column %q", name)
		}
	}
	var rows []row
	for {
		record, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse row: %w", err)
		}
		rows = append(rows, row{
			Provider: record[col["provider"]],
			Board:    record[col["board"]],
			Company:  record[col["company"]],
		})
	}
	return rows, nil
}

// openInput opens -in as a local file, or fetches it over HTTP(S) when it looks like a URL —
// so -in can point at a raw file hosted on GitHub, S3, a VPS, etc. instead of requiring a
// local copy first. A non-2xx response or a network error is returned as a plain error, same
// as a local os.Open failure, so callers handle both the same way.
func openInput(in string) (io.ReadCloser, error) {
	if strings.HasPrefix(in, "http://") || strings.HasPrefix(in, "https://") {
		client := &http.Client{Timeout: 60 * time.Second}
		resp, err := client.Get(in)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", in, err)
		}
		if resp.StatusCode/100 != 2 {
			resp.Body.Close()
			return nil, fmt.Errorf("fetch %s: unexpected status %s", in, resp.Status)
		}
		return resp.Body, nil
	}
	return os.Open(in)
}

func main() { worker.Main(run) }

func run() int {
	in := flag.String("in", "", "input CSV with provider,board,company columns (required)")
	apply := flag.Bool("apply", false, "actually write; without it the run only reports counts")
	onlyProvider := flag.String("provider", "", "restrict to a single provider (optional)")
	logEvery := flag.Int("log-every", 500, "print progress every N rows")
	flag.Parse()

	if *in == "" {
		log.Print("bulk-add-boards: -in is required")
		return 2
	}

	f, err := openInput(*in)
	if err != nil {
		log.Printf("bulk-add-boards: open %s: %v", *in, err)
		return 1
	}
	rows, err := parseCSV(f)
	f.Close()
	if err != nil {
		log.Printf("bulk-add-boards: %v", err)
		return 1
	}
	log.Printf("bulk-add-boards: %d rows loaded from %s", len(rows), *in)
	if *onlyProvider != "" {
		filtered := rows[:0]
		for _, r := range rows {
			if r.Provider == *onlyProvider {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
		log.Printf("bulk-add-boards: %d rows match provider=%s", len(rows), *onlyProvider)
	}

	registry := sources.Taxonomy()

	// Pre-validate everything before opening a DB connection at all — matches add-board's
	// own "validate, then decide whether to write" order, just amortized over the whole file
	// instead of per-invocation.
	var valid []row
	invalid := map[string]int{} // reason -> count
	for _, r := range rows {
		in := boardcatalog.InsertInput{Provider: r.Provider, Board: r.Board, Company: r.Company}
		if err := boardcatalog.Validate(in, registry); err != nil {
			invalid[err.Error()]++
			continue
		}
		valid = append(valid, r)
	}
	log.Printf("bulk-add-boards: %d valid, %d invalid", len(valid), len(rows)-len(valid))
	for reason, count := range invalid {
		log.Printf("  invalid (%dx): %s", count, reason)
	}

	if !*apply {
		log.Print("bulk-add-boards: dry run, nothing written. Re-run with --apply to add.")
		return 0
	}

	ctx, _, pool, cleanup, err := worker.Bootstrap(context.Background())
	if err != nil {
		log.Printf("bulk-add-boards: database: %v", err)
		return 1
	}
	defer cleanup()

	repo := boardcatalog.NewQueriesRepository(db.New(pool))
	inserter := boardcatalog.NewInserter(repo, registry)

	added, duplicate, failed := 0, 0, 0
	start := time.Now()
	for i, r := range valid {
		in := boardcatalog.InsertInput{Provider: r.Provider, Board: r.Board, Company: r.Company}
		_, err := inserter.Insert(ctx, in, boardcatalog.StatusActive)
		switch {
		case err == nil:
			added++
		case errors.Is(err, boardcatalog.ErrDuplicateBoard):
			duplicate++
		default:
			failed++
			log.Printf("bulk-add-boards: insert failed for %s/%s: %v", r.Provider, r.Board, err)
		}
		if (i+1)%*logEvery == 0 {
			log.Printf("bulk-add-boards: %d/%d processed (added=%d duplicate=%d failed=%d) elapsed=%s",
				i+1, len(valid), added, duplicate, failed, time.Since(start).Round(time.Second))
		}
	}

	log.Printf("bulk-add-boards: done. added=%d duplicate=%d failed=%d elapsed=%s",
		added, duplicate, failed, time.Since(start).Round(time.Second))
	return 0
}

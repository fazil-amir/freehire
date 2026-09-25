package main

import (
	"context"
	"database/sql"
	"log"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// DBStore is a READ-ONLY connection to freehire's own Postgres database —
// boardly-api's Go code never writes to it, ever. It exists only to
// answer "is this provider's board already in the boards table", since
// the CSV's own `added` column can't stay in sync across separate
// dev/prod databases that might each run their own boardly-api against
// the same CSV.
type DBStore struct {
	db *sql.DB
}

// NewDBStore opens a connection pool against databaseURL. sql.Open doesn't
// actually dial until the first query, so an unreachable database at
// startup doesn't fail this call — it surfaces later, per-query, exactly
// where the graceful-degrade path (resolveAddedCounts) already expects it.
func NewDBStore(databaseURL string) (*DBStore, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	return &DBStore{db: db}, nil
}

// liveBoard is one 'active' or 'pending' row of freehire's boards table —
// the identity add-board needs to retire it.
type liveBoard struct {
	Provider, Board, Region string
}

// liveBoards is boardly-api's ONE read query: every live board. Both
// AddedCounts and LiveBoards are derived from it, so the read stays a
// single statement however many views consume it.
func (s *DBStore) liveBoards(ctx context.Context) ([]liveBoard, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT provider, board, region
		FROM boards
		WHERE status IN ('active', 'pending')
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []liveBoard
	for rows.Next() {
		var b liveBoard
		if err := rows.Scan(&b.Provider, &b.Board, &b.Region); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// AddedCounts returns, per provider, how many of its board rows are
// currently 'active' or 'pending' in freehire's own boards table — the
// live, cross-environment-correct answer the CSV's own `added` column
// can no longer give (see resolveAddedCounts for the fallback when this
// fails).
func (s *DBStore) AddedCounts(ctx context.Context) (map[string]int, error) {
	boards, err := s.liveBoards(ctx)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, b := range boards {
		counts[b.Provider]++
	}
	return counts, nil
}

// LiveBoards returns provider's live boards — what "Remove provider"
// retires, one add-board call each.
func (s *DBStore) LiveBoards(ctx context.Context, provider string) ([]liveBoard, error) {
	boards, err := s.liveBoards(ctx)
	if err != nil {
		return nil, err
	}
	var out []liveBoard
	for _, b := range boards {
		if b.Provider == provider {
			out = append(out, b)
		}
	}
	return out, nil
}

// resolveAddedCounts is the one place every page reads "how added is each
// provider" from: Postgres when reachable (the live, correct answer), or
// the CSV's own `added` column — frozen since runAdd() stopped writing to
// it — as a stale-but-non-crashing fallback when it isn't. The second
// return value is a warning for a UI to show when the fallback
// was used, or "" when the DB answered.
func resolveAddedCounts(ctx context.Context, app *App) (map[string]int, string) {
	if app.db != nil {
		counts, err := app.db.AddedCounts(ctx)
		if err == nil {
			return counts, ""
		}
		log.Printf("db: added counts: %v", err)
	}
	counts := map[string]int{}
	for _, row := range app.csv.Rows() {
		if row.Added {
			counts[row.Provider]++
		}
	}
	return counts, "Couldn't reach the database — added status may be stale."
}

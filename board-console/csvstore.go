package main

import (
	"crypto/sha1"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// BoardRow is one line of combined_boards.csv. This must stay readable by
// freehire's own cmd/bulk-add-boards unchanged: that tool reads only
// provider/board/company by header name and ignores extra columns, so id and
// added are safe additions.
type BoardRow struct {
	ID       string
	Provider string
	Board    string
	Company  string
	Added    bool
}

// CSVStore holds the whole combined_boards.csv in memory and persists every
// change atomically (temp file + rename) so a crash mid-write can't corrupt
// it.
type CSVStore struct {
	path string

	mu   sync.RWMutex
	rows []BoardRow
}

func NewCSVStore(path string) (*CSVStore, error) {
	s := &CSVStore{path: path}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *CSVStore) load() error {
	f, err := os.Open(s.path)
	if err != nil {
		return fmt.Errorf("open %s: %w", s.path, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	idx := map[string]int{}
	for i, h := range header {
		idx[strings.TrimSpace(strings.ToLower(h))] = i
	}
	for _, want := range []string{"provider", "board", "company"} {
		if _, ok := idx[want]; !ok {
			return fmt.Errorf("combined_boards.csv missing required column %q", want)
		}
	}
	idIdx, hasID := idx["id"]
	addedIdx, hasAdded := idx["added"]

	var rows []BoardRow
	backfilled := false
	for {
		rec, err := r.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("read row: %w", err)
		}
		row := BoardRow{
			Provider: get(rec, idx["provider"]),
			Board:    get(rec, idx["board"]),
			Company:  get(rec, idx["company"]),
		}
		if hasID {
			row.ID = get(rec, idIdx)
		}
		if row.ID == "" {
			row.ID = boardRowID(row.Provider, row.Board, row.Company)
			backfilled = true
		}
		if hasAdded {
			row.Added = strings.EqualFold(strings.TrimSpace(get(rec, addedIdx)), "true")
		} else {
			backfilled = true
		}
		rows = append(rows, row)
	}
	s.mu.Lock()
	s.rows = rows
	s.mu.Unlock()

	if backfilled || !hasID || !hasAdded {
		if err := s.save(); err != nil {
			return fmt.Errorf("backfill save: %w", err)
		}
	}
	return nil
}

func get(rec []string, i int) string {
	if i < 0 || i >= len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[i])
}

func boardRowID(provider, board, company string) string {
	h := sha1.Sum([]byte(provider + "|" + board + "|" + company))
	return hex.EncodeToString(h[:])[:12]
}

// save writes the whole store to a temp file and renames it over the
// original — atomic, so a crash mid-write never corrupts the CSV.
func (s *CSVStore) save() error {
	s.mu.RLock()
	rows := make([]BoardRow, len(s.rows))
	copy(rows, s.rows)
	s.mu.RUnlock()

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, "combined_boards-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed

	w := csv.NewWriter(tmp)
	if err := w.Write([]string{"id", "provider", "board", "company", "added"}); err != nil {
		tmp.Close()
		return err
	}
	for _, row := range rows {
		added := "false"
		if row.Added {
			added = "true"
		}
		if err := w.Write([]string{row.ID, row.Provider, row.Board, row.Company, added}); err != nil {
			tmp.Close()
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.path)
}

// Rows returns a snapshot copy of every row.
func (s *CSVStore) Rows() []BoardRow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]BoardRow, len(s.rows))
	copy(out, s.rows)
	return out
}

// DistinctProviders returns every distinct provider name already present in
// the CSV, sorted — this is the dropdown source for "+ New provider" and for
// schedules, since only a known provider may be picked there.
func (s *CSVStore) DistinctProviders() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]bool{}
	var out []string
	for _, r := range s.rows {
		if !seen[r.Provider] {
			seen[r.Provider] = true
			out = append(out, r.Provider)
		}
	}
	sort.Strings(out)
	return out
}

// AddedProviders returns distinct providers that have at least one row with
// added=true — the pool schedules may target.
func (s *CSVStore) AddedProviders() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]bool{}
	var out []string
	for _, r := range s.rows {
		if r.Added && !seen[r.Provider] {
			seen[r.Provider] = true
			out = append(out, r.Provider)
		}
	}
	sort.Strings(out)
	return out
}

// boardKey is freehire's identity for a board — the boards table is UNIQUE
// on (provider, lower(board), region), and the CSV carries no region. Two
// CSV rows sharing a key are ONE board to freehire however their casing or
// company differs ("AeroVect" and "aerovect"), so every "how many boards
// does this provider have" count must go by key, never by row: counting
// rows left such a provider "partially added" forever, since the second
// row can never be added — freehire rejects it as a duplicate.
func boardKey(provider, board string) string {
	return provider + "\x00" + strings.ToLower(board)
}

// boardCounts is how many distinct boards (see boardKey) each provider has
// among rows — the denominator every "N of M added" figure is measured
// against.
func boardCounts(rows []BoardRow) map[string]int {
	seen := map[string]bool{}
	counts := map[string]int{}
	for _, r := range rows {
		k := boardKey(r.Provider, r.Board)
		if !seen[k] {
			seen[k] = true
			counts[r.Provider]++
		}
	}
	return counts
}

// HasBoard reports whether the CSV already holds this provider's board
// under freehire's identity rule — what "+ New provider" checks so it never
// appends a row freehire would reject as a duplicate.
func (s *CSVStore) HasBoard(provider, board string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k := boardKey(provider, board)
	for _, r := range s.rows {
		if boardKey(r.Provider, r.Board) == k {
			return true
		}
	}
	return false
}

// FullyAddedProviders returns the set of providers whose CSV rows are ALL
// marked added=true — the single source of truth for "already added"
// across the app. A provider with even one un-added row is NOT in this
// set, since treating it as fully added (as an earlier version of the
// bulk-crawl path did, by checking for ANY added row) silently left its
// remaining rows un-added forever.
func (s *CSVStore) FullyAddedProviders() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := boardCounts(s.rows)
	// A board counts as added when any of its rows is — see boardKey.
	addedKeys := map[string]bool{}
	added := map[string]int{}
	for _, r := range s.rows {
		k := boardKey(r.Provider, r.Board)
		if r.Added && !addedKeys[k] {
			addedKeys[k] = true
			added[r.Provider]++
		}
	}
	fully := map[string]bool{}
	for p, t := range total {
		if t > 0 && t == added[p] {
			fully[p] = true
		}
	}
	return fully
}

// AppendRow adds a brand-new row (the "+ New provider" form) and persists.
func (s *CSVStore) AppendRow(provider, board, company string, added bool) error {
	s.mu.Lock()
	s.rows = append(s.rows, BoardRow{
		ID:       boardRowID(provider, board, company),
		Provider: provider,
		Board:    board,
		Company:  company,
		Added:    added,
	})
	s.mu.Unlock()
	return s.save()
}

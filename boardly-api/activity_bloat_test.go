package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestTrimActivityFile_LineCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.jsonl")

	var sb strings.Builder
	for i := 0; i < 50; i++ {
		sb.WriteString(`{"ID":` + strconv.Itoa(i) + `}` + "\n")
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	trimActivityFile(path, 10, 50*1024*1024)

	got := countLines(path)
	if got != 10 {
		t.Fatalf("want 10 lines kept, got %d", got)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"ID":49`) {
		t.Fatal("want the newest line kept")
	}
	if strings.Contains(string(data), `"ID":39`) {
		t.Fatal("want the oldest surviving line to be #40, not #39 (off-by-one)")
	}
}

func TestTrimActivityFile_ByteCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.jsonl")

	// 5 lines of ~100 bytes each — well under the line cap, but the byte
	// cap should still force a trim down to what fits.
	line := `{"ID":1,"Stdout":"` + strings.Repeat("x", 90) + `"}` + "\n"
	if err := os.WriteFile(path, []byte(strings.Repeat(line, 5)), 0o644); err != nil {
		t.Fatal(err)
	}
	lineLen := int64(len(line))

	trimActivityFile(path, 10_000, lineLen*2) // budget for ~2 lines

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > lineLen*2 {
		t.Fatalf("want file within byte budget (%d), got %d bytes", lineLen*2, info.Size())
	}
	if info.Size() == 0 {
		t.Fatal("want at least the newest line kept, got an empty file")
	}
}

func TestCapOutput_KeepsTailUnderLimit(t *testing.T) {
	big := strings.Repeat("a", runOutputMaxBytes+1000)
	out := capOutput(big)
	if len(out) > runOutputMaxBytes+100 { // small allowance for the marker prefix
		t.Fatalf("want capped output near %d bytes, got %d", runOutputMaxBytes, len(out))
	}
	if !strings.HasSuffix(out, "a") {
		t.Fatal("want the tail of the original content preserved")
	}
	if !strings.Contains(out, "truncated") {
		t.Fatal("want a truncation marker when content was dropped")
	}
}

func TestCapOutput_NoOpUnderLimit(t *testing.T) {
	small := "hello world"
	if capOutput(small) != small {
		t.Fatal("want output under the cap returned unchanged")
	}
}

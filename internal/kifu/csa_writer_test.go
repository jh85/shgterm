package kifu

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFormatCSA(t *testing.T) {
	r := Record{
		GameID:        "test-123",
		BlackName:     "alice",
		WhiteName:     "bob",
		StartedAt:     time.Date(2026, 4, 18, 10, 30, 0, 0, time.UTC),
		EndedAt:       time.Date(2026, 4, 18, 10, 45, 0, 0, time.UTC),
		TimeLimitSec:  900,
		ByoyomiSec:    10,
		InitialCSAPos: "PI\n+\n",
		MoveLines:     []string{"+7776FU,T10", "-3334FU,T8"},
		Terminator:    "%TORYO",
	}
	out := string(FormatCSA(r))
	for _, want := range []string{
		"V2.2", "N+alice", "N-bob", "$EVENT:test-123",
		"$START_TIME:2026/04/18 10:30:00",
		"$TIME_LIMIT:00:15+10",
		"PI", "+", "+7776FU,T10", "-3334FU,T8", "%TORYO",
		"$END_TIME:2026/04/18 10:45:00",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderFilenameSanitizes(t *testing.T) {
	r := Record{GameID: "evil/name", BlackName: "a", WhiteName: "b",
		StartedAt: time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC)}
	got := RenderFilename("{datetime}{_title}", r)
	if strings.Contains(got, "/") {
		t.Fatalf("unsanitized: %q", got)
	}
	if !strings.Contains(got, "20260418_100000") {
		t.Fatalf("no datetime: %q", got)
	}
}

func TestWriteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	r := Record{
		GameID: "g1", BlackName: "alice", WhiteName: "bob",
		StartedAt:     time.Now(),
		InitialCSAPos: "PI\n+\n",
		MoveLines:     []string{"+7776FU,T10"},
		Terminator:    "%TORYO",
	}
	path, err := Write(dir, "", r)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(path) != ".csa" {
		t.Fatalf("ext: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "+7776FU,T10") {
		t.Fatalf("missing move in file:\n%s", string(data))
	}
}

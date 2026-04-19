package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"

	"github.com/jh85/shgterm/internal/csa"
	"github.com/jh85/shgterm/internal/shogi"
	"github.com/jh85/shgterm/internal/usi"
)

// dumpScreen returns the simulation screen contents as \n-separated lines.
// Wide runes occupy two cells in the grid but a single rune in the line;
// we skip the continuation cell using runewidth.
func dumpScreen(s tcell.SimulationScreen) string {
	cells, w, h := s.GetContents()
	var b strings.Builder
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := cells[y*w+x]
			if len(c.Runes) == 0 {
				b.WriteRune(' ')
				continue
			}
			r := c.Runes[0]
			if r == 0 {
				r = ' '
			}
			b.WriteRune(r)
			if runewidth.RuneWidth(r) == 2 {
				x++
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func TestRenderStartPosition(t *testing.T) {
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(100, 30)

	ui := newWithScreen(sim, func() {}, false, false)
	ui.SetEngine("FakeEngine", "bench")
	ui.SetGame(&csa.GameSummary{
		ID:              "test-abc",
		ProtocolVersion: "v121_floodgate",
		Players: [2]csa.PlayerInfo{
			{Name: "alice", Time: csa.TimeConfig{TimeUnit: time.Second, TotalTimeUnits: 900, ByoyomiUnits: 10}},
			{Name: "bob", Time: csa.TimeConfig{TimeUnit: time.Second, TotalTimeUnits: 900, ByoyomiUnits: 10}},
		},
		MyColor: csa.Black, ToMove: csa.Black,
	}, csa.Black)
	ui.SetPosition(shogi.NewStartPosition(), "", "")
	ui.SetClock(900, 900, time.Second)
	ui.LogLine("info", "engine handshake ok")
	ui.LogLine("info", "LOGIN ok")
	ui.SetPV(usi.Info{Depth: 18, Score: 420, ScoreCP: true, Nodes: 12_400_000, NPS: 2_100_000, PV: []string{"7g7f", "3c3d", "2g2f"}})

	ui.render()
	out := dumpScreen(sim)

	for _, needle := range []string{
		"shgterm",
		"engine: FakeEngine",
		"game: test-abc",
		"state: playing",
		"Clock",
		"先手",
		"後手",
		"alice",
		"bob",
		"Engine PV",
		"+420cp",
		"Time control",
		"持ち時間",
		"LOGIN ok",
		"engine handshake ok",
	} {
		if !strings.Contains(out, needle) {
			t.Errorf("missing %q in render:\n%s", needle, out)
		}
	}
	// Kanji pieces should appear (lance=香 on back rank).
	if !strings.Contains(out, "香") || !strings.Contains(out, "玉") {
		t.Errorf("missing kanji in render")
	}
}

func TestRenderASCIIMode(t *testing.T) {
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(100, 30)

	ui := newWithScreen(sim, func() {}, true /*ascii*/, false)
	ui.SetPosition(shogi.NewStartPosition(), "", "")
	ui.render()
	out := dumpScreen(sim)

	if strings.Contains(out, "香") {
		t.Errorf("ascii mode should not contain kanji")
	}
	if !strings.Contains(out, "L") || !strings.Contains(out, "K") {
		t.Errorf("ascii mode should contain USI letters L/K")
	}
}

func TestRenderFlipSwapsRanks(t *testing.T) {
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(100, 30)

	ui := newWithScreen(sim, func() {}, false, true /*flip*/)
	ui.SetPosition(shogi.NewStartPosition(), "", "")
	ui.render()
	out := dumpScreen(sim)
	// When flipped, rank 'a' must appear near the bottom of the board,
	// and rank 'i' near the top. Simple invariant: locate first ' i' then
	// later ' a' (by linear index in the dumped string).
	iIdx := strings.Index(out, " i")
	aIdx := strings.LastIndex(out, " a")
	if iIdx < 0 || aIdx < 0 || iIdx >= aIdx {
		t.Fatalf("flip did not reorder ranks: out=\n%s", out)
	}
}

func TestRenderCompactBelowMinSize(t *testing.T) {
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(60, 20)

	ui := newWithScreen(sim, func() {}, false, false)
	ui.render() // must not panic
	out := dumpScreen(sim)
	if !strings.Contains(out, "too small") {
		t.Errorf("compact fallback missing, got:\n%s", out)
	}
}

package tui

import (
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/jh85/shgterm/internal/csa"
	"github.com/jh85/shgterm/internal/shogi"
	"github.com/jh85/shgterm/internal/usi"
)

// TestRenderDumpStartPosition prints the TUI to stdout for visual inspection.
// Run with: go test -v ./internal/tui -run TestRenderDump
func TestRenderDumpStartPosition(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(100, 30)

	ui := newWithScreen(sim, func() {}, false, false)
	ui.SetEngine("YaneuraOu 9.00", "Yaneurao")
	ui.SetGame(&csa.GameSummary{
		ID:              "test-20260418-0001",
		ProtocolVersion: "v121_floodgate",
		Players: [2]csa.PlayerInfo{
			{Name: "alice", Time: csa.TimeConfig{TimeUnit: time.Second, TotalTimeUnits: 900, ByoyomiUnits: 10}},
			{Name: "bob", Time: csa.TimeConfig{TimeUnit: time.Second, TotalTimeUnits: 900, ByoyomiUnits: 10}},
		},
		MyColor: csa.Black, ToMove: csa.Black,
	}, csa.Black)

	// Apply a single pawn push to exercise last-move highlight.
	p := shogi.NewStartPosition()
	m, _ := shogi.ParseUSIMove("7g7f")
	_ = p.Apply(m)
	ui.SetPosition(p, "+7776FU,T10", "7g7f")

	ui.SetClock(887, 900, time.Second)
	// Simulate that White's clock just started counting down 5s ago.
	ui.SetTurnTimer(csa.White, time.Now().Add(-5*time.Second))
	ui.SetPV(usi.Info{
		Depth: 18, Score: 420, ScoreCP: true,
		Nodes: 12_400_000, NPS: 2_100_000,
		PV: []string{"3c3d", "2g2f", "8c8d", "7i6h", "3d3e", "4a3b", "6i7h", "9c9d"},
	})
	ui.LogLine("info", "engine handshake ok (YaneuraOu 9.00)")
	ui.LogLine("info", "csa login ok as alice")
	ui.LogLine("info", "game start: you are Black")
	ui.LogLine("info", "> +7776FU,'* 420 7g7f 3c3d 2g2f")
	ui.LogLine("info", "< opponent thinking...")

	ui.render()

	t.Logf("=== simulated screen (100x30) ===\n%s", dumpScreen(sim))
}

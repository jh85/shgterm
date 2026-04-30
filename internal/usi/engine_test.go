package usi

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func enginePath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("testdata/fake_engine.sh")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHandshakeAndGo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	e := New(Options{Path: enginePath(t), Name: "fake"})
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = e.Quit(context.Background()) })

	if err := e.Handshake(ctx, []Setoption{
		{Name: "USI_Hash", Value: "64"},
		{Name: "MultiPV", Value: "1"},
	}); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if e.IDName() != "FakeEngine" {
		t.Fatalf("id name = %q", e.IDName())
	}
	if err := e.NewGame(); err != nil {
		t.Fatalf("newgame: %v", err)
	}

	ch, err := e.Go(ctx, "position startpos", TimeControl{BTime: 60000, WTime: 60000, Byoyomi: 10000})
	if err != nil {
		t.Fatalf("go: %v", err)
	}

	var gotBest bool
	var infoCount int
	for ev := range ch {
		if ev.Error != nil {
			t.Fatalf("event error: %v", ev.Error)
		}
		if ev.Info != nil {
			infoCount++
		}
		if ev.BestMove != nil {
			if ev.BestMove.Move != "7g7f" {
				t.Fatalf("bestmove = %q, want 7g7f", ev.BestMove.Move)
			}
			if ev.BestMove.Ponder != "3c3d" {
				t.Fatalf("ponder = %q, want 3c3d", ev.BestMove.Ponder)
			}
			gotBest = true
		}
	}
	if !gotBest {
		t.Fatal("did not receive bestmove")
	}
	if infoCount < 2 {
		t.Fatalf("got %d info lines, want >= 2", infoCount)
	}

	if err := e.Gameover("win"); err != nil {
		t.Fatalf("gameover: %v", err)
	}
	if err := e.Quit(ctx); err != nil {
		t.Fatalf("quit: %v", err)
	}
}

func TestParseInfo(t *testing.T) {
	i, err := ParseInfo("info depth 18 seldepth 25 score cp -45 nodes 12000000 nps 2000000 time 6000 pv 7g7f 3c3d 2g2f")
	if err != nil {
		t.Fatal(err)
	}
	if i.Depth != 18 || i.SelDepth != 25 || i.Score != -45 || !i.ScoreCP {
		t.Fatalf("scalar fields: %+v", i)
	}
	if i.Nodes != 12000000 || i.NPS != 2000000 || i.TimeMS != 6000 {
		t.Fatalf("numeric fields: %+v", i)
	}
	if len(i.PV) != 3 || i.PV[0] != "7g7f" || i.PV[2] != "2g2f" {
		t.Fatalf("pv: %+v", i.PV)
	}
}

func TestParseInfoMate(t *testing.T) {
	i, err := ParseInfo("info depth 3 score mate 5 pv 7g7f 3c3d 2b3c+")
	if err != nil {
		t.Fatal(err)
	}
	if i.ScoreMate != 5 {
		t.Fatalf("mate = %d, want 5", i.ScoreMate)
	}
	if i.ScoreCP {
		t.Fatal("should not be flagged as cp")
	}
}

func TestFormatGoVariants(t *testing.T) {
	tc := TimeControl{BTime: 60000, WTime: 60000, Byoyomi: 10000}
	if got := tc.FormatGo(); got != "btime 60000 wtime 60000 byoyomi 10000" {
		t.Fatalf("byoyomi form: %q", got)
	}
	tc2 := TimeControl{BTime: 300000, WTime: 250000, BInc: 1000, WInc: 1000}
	if got := tc2.FormatGo(); got != "btime 300000 wtime 250000 binc 1000 winc 1000" {
		t.Fatalf("increment form: %q", got)
	}
	tc3 := TimeControl{Infinite: true}
	if got := tc3.FormatGo(); got != "infinite" {
		t.Fatalf("infinite: %q", got)
	}
}

func TestFormatPositionStartpos(t *testing.T) {
	const hirate = "lnsgkgsnl/1r5b1/ppppppppp/9/9/9/PPPPPPPPP/1B5R1/LNSGKGSNL b - 1"
	if got := FormatPosition(hirate, nil); got != "position startpos" {
		t.Fatalf("%q", got)
	}
	if got := FormatPosition(hirate, []string{"7g7f", "3c3d"}); got != "position startpos moves 7g7f 3c3d" {
		t.Fatalf("%q", got)
	}
	custom := "9/9/9/9/9/9/9/9/4K4 b - 1"
	if got := FormatPosition(custom, []string{"5i5h"}); got != "position sfen "+custom+" moves 5i5h" {
		t.Fatalf("%q", got)
	}
}

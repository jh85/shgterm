package shogi

import (
	"strings"
	"testing"
)

const startSFEN = "lnsgkgsnl/1r5b1/ppppppppp/9/9/9/PPPPPPPPP/1B5R1/LNSGKGSNL b - 1"

func TestStartPositionSFEN(t *testing.T) {
	p := NewStartPosition()
	got := p.SFEN()
	if got != startSFEN {
		t.Fatalf("start SFEN mismatch:\n got %q\nwant %q", got, startSFEN)
	}
}

func TestSFENRoundTripStart(t *testing.T) {
	p, err := ParseSFEN(startSFEN)
	if err != nil {
		t.Fatal(err)
	}
	if p.SFEN() != startSFEN {
		t.Fatalf("round-trip mismatch: %q", p.SFEN())
	}
}

func TestApplyOpeningMoves(t *testing.T) {
	p := NewStartPosition()
	// 1. ▲7六歩 (7g7f)
	m, err := ParseUSIMove("7g7f")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(m); err != nil {
		t.Fatalf("apply 7g7f: %v", err)
	}
	if p.Turn != White {
		t.Fatal("turn should flip to White after black move")
	}
	if p.At(Square{File: 7, Rank: 6}).Type != Pawn {
		t.Fatal("pawn did not land on 7f")
	}
	if !p.At(Square{File: 7, Rank: 7}).IsEmpty() {
		t.Fatal("7g should be empty")
	}

	// 2. △3四歩 (3c3d)
	m, _ = ParseUSIMove("3c3d")
	if err := p.Apply(m); err != nil {
		t.Fatal(err)
	}
	if p.Turn != Black {
		t.Fatal("turn should be Black")
	}
	if p.Ply != 3 {
		t.Fatalf("ply = %d, want 3", p.Ply)
	}
}

func TestCaptureGoesToHand(t *testing.T) {
	// Set up a minimal position: black pawn on 5e, white pawn on 5d, black to move.
	// Construct via SFEN for brevity.
	sfen := "9/9/9/4p4/4P4/9/9/9/4K4 b - 1"
	// Files in SFEN rank go 9..1 left-to-right, so the 'P' in ...4P4... is at file 5.
	// Rank "4p4" is rank 4 (= 'd'), "4P4" is rank 5 (= 'e').
	p, err := ParseSFEN(sfen)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := ParseUSIMove("5e5d+") // capture with promotion to +P
	if err := p.Apply(m); err != nil {
		t.Fatal(err)
	}
	if p.Hands[Black][Pawn] != 1 {
		t.Fatalf("captured pawn not in hand: hands=%+v", p.Hands[Black])
	}
	if p.At(Square{File: 5, Rank: 4}).Type != PPawn {
		t.Fatalf("promoted pawn not on 5d: got %+v", p.At(Square{File: 5, Rank: 4}))
	}
}

func TestUSIToCSAAndBack_Normal(t *testing.T) {
	p := NewStartPosition()
	m, _ := ParseUSIMove("7g7f")
	csa, err := p.MoveToCSA(m, p.Turn)
	if err != nil {
		t.Fatal(err)
	}
	if csa != "+7776FU" {
		t.Fatalf("CSA = %q, want +7776FU", csa)
	}
	back, err := p.CSAToMove(csa)
	if err != nil {
		t.Fatal(err)
	}
	if back.FormatUSI() != "7g7f" {
		t.Fatalf("USI = %q, want 7g7f", back.FormatUSI())
	}
}

func TestUSIToCSAPromotion(t *testing.T) {
	// Black pawn on 3d captures and promotes on 3c.
	sfen := "9/9/4p4/2P4/9/9/9/9/4K4 b - 1"
	// "2P4" has 2 empties (files 9,8), P at file 7? Let me fix below.
	_ = sfen
	// Build more simply: place a black pawn at 3d and a white pawn at 3c.
	p, err := ParseSFEN("9/9/6p2/6P2/9/9/9/9/4K4 b - 1")
	if err != nil {
		t.Fatal(err)
	}
	// "6p2" = 6 empty (files 9..4), 'p' at file 3, 2 empty (files 2,1). rank idx 2 = rank 3.
	// "6P2" at rank idx 3 = rank 4.
	m, _ := ParseUSIMove("3d3c+")
	csa, err := p.MoveToCSA(m, p.Turn)
	if err != nil {
		t.Fatal(err)
	}
	if csa != "+3433TO" {
		t.Fatalf("CSA = %q, want +3433TO", csa)
	}
	// Round-trip via CSA: the CSA string carries the promoted piece code
	// (TO), and CSAToMove should infer Promote=true.
	back, err := p.CSAToMove(csa)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Promote {
		t.Fatal("promotion flag not inferred")
	}
	if back.FormatUSI() != "3d3c+" {
		t.Fatalf("USI = %q, want 3d3c+", back.FormatUSI())
	}
}

func TestDropRoundTrip(t *testing.T) {
	// Put a pawn in black's hand, empty board (+ kings so SFEN is plausible).
	p, err := ParseSFEN("4k4/9/9/9/9/9/9/9/4K4 b P 1")
	if err != nil {
		t.Fatal(err)
	}
	m, _ := ParseUSIMove("P*5e")
	csa, err := p.MoveToCSA(m, p.Turn)
	if err != nil {
		t.Fatal(err)
	}
	if csa != "+0055FU" {
		t.Fatalf("CSA = %q, want +0055FU", csa)
	}
	back, err := p.CSAToMove(csa)
	if err != nil {
		t.Fatal(err)
	}
	if !back.IsDrop() || back.Drop != Pawn || back.To.File != 5 || back.To.Rank != 5 {
		t.Fatalf("round-tripped drop wrong: %+v", back)
	}
}

func TestCSAPositionPI(t *testing.T) {
	p, err := ParseCSAPosition("PI\n+")
	if err != nil {
		t.Fatal(err)
	}
	if p.SFEN() != startSFEN {
		t.Fatalf("PI SFEN = %q, want %q", p.SFEN(), startSFEN)
	}
}

func TestCSAPositionExplicit(t *testing.T) {
	block := `P1-KY-KE-GI-KI-OU-KI-GI-KE-KY
P2 * -HI *  *  *  *  * -KA *
P3-FU-FU-FU-FU-FU-FU-FU-FU-FU
P4 *  *  *  *  *  *  *  *  *
P5 *  *  *  *  *  *  *  *  *
P6 *  *  *  *  *  *  *  *  *
P7+FU+FU+FU+FU+FU+FU+FU+FU+FU
P8 * +KA *  *  *  *  * +HI *
P9+LK+LK+GI+KI+OU+KI+GI+KE+KY
P+
P-
+
`
	// Intentional typo (LK) in P9 to verify error path.
	if _, err := ParseCSAPosition(block); err == nil {
		t.Fatal("expected error on bad piece code")
	}

	good := strings.Replace(block, "+LK+LK+GI", "+KY+KE+GI", 1)
	p, err := ParseCSAPosition(good)
	if err != nil {
		t.Fatal(err)
	}
	if p.SFEN() != startSFEN {
		t.Fatalf("explicit SFEN = %q, want %q", p.SFEN(), startSFEN)
	}
}

func TestCSAPositionWithHand(t *testing.T) {
	block := `PI
P+00FU00FU
P-00KA
+
`
	p, err := ParseCSAPosition(block)
	if err != nil {
		t.Fatal(err)
	}
	if p.Hands[Black][Pawn] != 2 {
		t.Fatalf("black pawns in hand = %d, want 2", p.Hands[Black][Pawn])
	}
	if p.Hands[White][Bishop] != 1 {
		t.Fatalf("white bishops in hand = %d, want 1", p.Hands[White][Bishop])
	}
}

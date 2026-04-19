package shogi

import "fmt"

// Position is a shogi position: 9x9 board, hands per color, side to move,
// and ply count (starts at 1). Apply assumes moves are legal.
type Position struct {
	// Board is indexed as board[rank-1][file-1].
	// rank 1 = top (White's back / USI 'a'); file 1 = rightmost.
	Board [9][9]Piece
	// Hands[color][piece] holds the count of that piece in that color's hand.
	// Only Pawn..Rook indices are used.
	Hands [2][King]int
	Turn  Color
	// Ply is the 1-based move number (matches SFEN's trailing number).
	Ply int
}

// NewStartPosition returns the standard hirate starting position.
func NewStartPosition() *Position {
	// Ranks given top-to-bottom (1..9): rank 1 = white back row, rank 9 = black back row.
	initial := [9][9]PieceType{
		// Board[rank-1][file-1]: index 0 = file 1 (right), index 8 = file 9 (left).
		// Standard hirate: white rook on file 8 / bishop on file 2;
		// black rook on file 2 / bishop on file 8 (180° rotational).
		{Lance, Knight, Silver, Gold, King, Gold, Silver, Knight, Lance}, // rank 1 (white back)
		{Empty, Bishop, Empty, Empty, Empty, Empty, Empty, Rook, Empty},  // rank 2 (white bishop@2 / rook@8)
		{Pawn, Pawn, Pawn, Pawn, Pawn, Pawn, Pawn, Pawn, Pawn},           // rank 3 (white pawns)
		{Empty, Empty, Empty, Empty, Empty, Empty, Empty, Empty, Empty},
		{Empty, Empty, Empty, Empty, Empty, Empty, Empty, Empty, Empty},
		{Empty, Empty, Empty, Empty, Empty, Empty, Empty, Empty, Empty},
		{Pawn, Pawn, Pawn, Pawn, Pawn, Pawn, Pawn, Pawn, Pawn},           // rank 7 (black pawns)
		{Empty, Rook, Empty, Empty, Empty, Empty, Empty, Bishop, Empty},  // rank 8 (black rook@2 / bishop@8)
		{Lance, Knight, Silver, Gold, King, Gold, Silver, Knight, Lance}, // rank 9 (black back)
	}
	p := &Position{Turn: Black, Ply: 1}
	for r := 0; r < 9; r++ {
		for f := 0; f < 9; f++ {
			pt := initial[r][f]
			if pt == Empty {
				continue
			}
			color := White
			if r >= 6 { // ranks 7-9 are black's side
				color = Black
			}
			p.Board[r][f] = Piece{Type: pt, Color: color}
		}
	}
	return p
}

// Clone returns a deep copy of p.
func (p *Position) Clone() *Position {
	cp := *p
	return &cp
}

// At returns the piece at sq.
func (p *Position) At(sq Square) Piece {
	return p.Board[sq.Rank-1][sq.File-1]
}

func (p *Position) set(sq Square, pc Piece) {
	p.Board[sq.Rank-1][sq.File-1] = pc
}

// Move describes an application-ready move. Drop != Empty means a drop;
// otherwise From/To/Promote are used.
type Move struct {
	From    Square
	To      Square
	Promote bool
	Drop    PieceType // Empty unless this is a drop
}

// IsDrop reports whether m is a drop move.
func (m Move) IsDrop() bool { return m.Drop != Empty }

// Apply mutates p by applying m. It does no legality checking; callers
// must supply only moves the engine or server has accepted. After return
// the side to move is flipped and Ply is incremented.
func (p *Position) Apply(m Move) error {
	if m.IsDrop() {
		if !m.To.IsValid() {
			return fmt.Errorf("drop target invalid: %+v", m.To)
		}
		if !m.Drop.IsDroppable() {
			return fmt.Errorf("drop type not droppable: %d", m.Drop)
		}
		if p.Hands[p.Turn][m.Drop] <= 0 {
			return fmt.Errorf("no %s in %s's hand", pieceTypeCSA[m.Drop], colorName(p.Turn))
		}
		if !p.At(m.To).IsEmpty() {
			return fmt.Errorf("drop onto occupied square %+v", m.To)
		}
		p.set(m.To, Piece{Type: m.Drop, Color: p.Turn})
		p.Hands[p.Turn][m.Drop]--
	} else {
		if !m.From.IsValid() || !m.To.IsValid() {
			return fmt.Errorf("move squares invalid: from=%+v to=%+v", m.From, m.To)
		}
		mover := p.At(m.From)
		if mover.IsEmpty() {
			return fmt.Errorf("no piece on from square %+v", m.From)
		}
		if mover.Color != p.Turn {
			return fmt.Errorf("moving opponent's piece at %+v", m.From)
		}
		captured := p.At(m.To)
		if !captured.IsEmpty() {
			if captured.Color == p.Turn {
				return fmt.Errorf("capturing own piece at %+v", m.To)
			}
			p.Hands[p.Turn][captured.Type.Unpromoted()]++
		}
		if m.Promote {
			mover.Type = mover.Type.Promoted()
		}
		p.set(m.From, Piece{})
		p.set(m.To, mover)
	}
	p.Turn = p.Turn.Opponent()
	p.Ply++
	return nil
}

func colorName(c Color) string {
	if c == Black {
		return "black"
	}
	return "white"
}

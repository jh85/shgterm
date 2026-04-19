package shogi

import (
	"fmt"
	"strings"
)

// ParseUSIMove parses a USI move notation ("7g7f", "7g7f+", "P*5f") into a
// Move. It does not consult any Position; conversion to CSA requires a
// Position for piece lookup — see (*Position).USIToCSAMove.
func ParseUSIMove(s string) (Move, error) {
	s = strings.TrimSpace(s)
	if len(s) < 4 {
		return Move{}, fmt.Errorf("usi-move: too short: %q", s)
	}
	// Drop: "P*5f"
	if s[1] == '*' {
		pt, ok := pieceTypeFromSFEN[upper(s[0])]
		if !ok || !pt.IsDroppable() {
			return Move{}, fmt.Errorf("usi-move: bad drop piece %q", string(s[0]))
		}
		to, ok := SquareFromUSI(s[2:4])
		if !ok {
			return Move{}, fmt.Errorf("usi-move: bad drop square %q", s[2:4])
		}
		if len(s) != 4 {
			return Move{}, fmt.Errorf("usi-move: trailing chars after drop: %q", s)
		}
		return Move{To: to, Drop: pt}, nil
	}

	from, ok := SquareFromUSI(s[0:2])
	if !ok {
		return Move{}, fmt.Errorf("usi-move: bad from %q", s[0:2])
	}
	to, ok := SquareFromUSI(s[2:4])
	if !ok {
		return Move{}, fmt.Errorf("usi-move: bad to %q", s[2:4])
	}
	m := Move{From: from, To: to}
	if len(s) == 5 {
		if s[4] != '+' {
			return Move{}, fmt.Errorf("usi-move: bad trailing char %q", string(s[4]))
		}
		m.Promote = true
	} else if len(s) != 4 {
		return Move{}, fmt.Errorf("usi-move: unexpected length: %q", s)
	}
	return m, nil
}

// FormatUSI returns the USI notation for m.
func (m Move) FormatUSI() string {
	if m.IsDrop() {
		return fmt.Sprintf("%s*%d%c", pieceTypeSFEN[m.Drop], m.To.File, m.To.USIRankLetter())
	}
	s := fmt.Sprintf("%d%c%d%c", m.From.File, m.From.USIRankLetter(), m.To.File, m.To.USIRankLetter())
	if m.Promote {
		s += "+"
	}
	return s
}

// ParseCSAMove parses a CSA move string like "+7776FU" or "-0055FU". It
// returns a Move, the side that made it, the after-move piece type (as
// encoded in the CSA string), and an error.
//
// Callers that need to detect promotion must compare the returned
// afterType against the actual piece at the From square (see
// (*Position).CSAToMove which does this).
func ParseCSAMove(s string) (m Move, side Color, afterType PieceType, err error) {
	s = strings.TrimSpace(s)
	if len(s) != 7 {
		return Move{}, 0, Empty, fmt.Errorf("csa-move: need 7 chars, got %q", s)
	}
	switch s[0] {
	case '+':
		side = Black
	case '-':
		side = White
	default:
		return Move{}, 0, Empty, fmt.Errorf("csa-move: bad sign %q", string(s[0]))
	}
	from, ok := SquareFromCSA(s[1:3])
	if !ok {
		return Move{}, 0, Empty, fmt.Errorf("csa-move: bad from %q", s[1:3])
	}
	to, ok := SquareFromCSA(s[3:5])
	if !ok || !to.IsValid() {
		return Move{}, 0, Empty, fmt.Errorf("csa-move: bad to %q", s[3:5])
	}
	pt, ok := pieceTypeFromCSA[s[5:7]]
	if !ok {
		return Move{}, 0, Empty, fmt.Errorf("csa-move: bad piece code %q", s[5:7])
	}
	afterType = pt
	if s[1:3] == "00" {
		if !pt.IsDroppable() {
			return Move{}, 0, Empty, fmt.Errorf("csa-move: drop of non-droppable %q", s[5:7])
		}
		m = Move{To: to, Drop: pt}
	} else {
		m = Move{From: from, To: to}
	}
	return m, side, afterType, nil
}

// CSAToMove converts a CSA move string to a Move, inferring the Promote
// flag by comparing the after-move piece code against the piece currently
// on the from-square. The side in the CSA string must match p.Turn.
func (p *Position) CSAToMove(s string) (Move, error) {
	m, side, afterType, err := ParseCSAMove(s)
	if err != nil {
		return Move{}, err
	}
	if side != p.Turn {
		return Move{}, fmt.Errorf("csa-move: side mismatch (move %c, turn %c)", side.CSASign(), p.Turn.CSASign())
	}
	if m.IsDrop() {
		return m, nil
	}
	mover := p.At(m.From)
	if mover.IsEmpty() {
		return Move{}, fmt.Errorf("csa-move: no piece on from square %+v", m.From)
	}
	if afterType != mover.Type {
		// The move results in a different piece type than is standing on
		// the from-square. The only legal way this happens is promotion.
		if afterType == mover.Type.Promoted() {
			m.Promote = true
		} else {
			return Move{}, fmt.Errorf("csa-move: piece type mismatch (from has %s, csa says %s)",
				pieceTypeCSA[mover.Type], pieceTypeCSA[afterType])
		}
	}
	return m, nil
}

// MoveToCSA formats m as a CSA move string for the given side. The Position
// is consulted only for non-drop moves to determine the after-move piece
// code (which depends on the piece currently at m.From and m.Promote).
func (p *Position) MoveToCSA(m Move, side Color) (string, error) {
	var code string
	if m.IsDrop() {
		code = pieceTypeCSA[m.Drop]
		return fmt.Sprintf("%c00%d%d%s", side.CSASign(), m.To.File, m.To.Rank, code), nil
	}
	mover := p.At(m.From)
	if mover.IsEmpty() {
		return "", fmt.Errorf("move-to-csa: no piece on from %+v", m.From)
	}
	t := mover.Type
	if m.Promote {
		t = t.Promoted()
	}
	code = pieceTypeCSA[t]
	return fmt.Sprintf("%c%d%d%d%d%s",
		side.CSASign(), m.From.File, m.From.Rank, m.To.File, m.To.Rank, code), nil
}

func upper(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - ('a' - 'A')
	}
	return c
}

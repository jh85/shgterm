package shogi

import (
	"fmt"
	"strings"
)

// ParseCSAPosition parses a CSA-style position block as found inside a
// Game_Summary's Position section. It accepts either:
//
//	PI [<square>,<piece> ...]       (hirate minus specified squares)
//	P1..P9 rank lines
//
// followed by optional P+/P- hand lines, and a final '+' or '-' side-to-move
// line. Leading/trailing whitespace and blank lines are ignored.
//
// The returned Position's Ply is 1.
func ParseCSAPosition(block string) (*Position, error) {
	lines := strings.Split(block, "\n")
	var pos *Position
	sideSet := false

	for _, raw := range lines {
		line := strings.TrimRight(strings.TrimLeft(raw, " \t"), " \t\r")
		if line == "" {
			continue
		}
		switch {
		case line == "PI" || strings.HasPrefix(line, "PI"):
			if pos != nil {
				return nil, fmt.Errorf("csa-pos: mixed PI with P1..P9")
			}
			pos = NewStartPosition()
			pos.Ply = 1
			// Optional "PI<sq1><piece1>,<sq2><piece2>..." removes pieces from hirate.
			rest := strings.TrimPrefix(line, "PI")
			rest = strings.TrimSpace(rest)
			if rest != "" {
				for _, tok := range strings.Split(rest, ",") {
					tok = strings.TrimSpace(tok)
					if len(tok) != 4 {
						return nil, fmt.Errorf("csa-pos: bad PI removal token %q", tok)
					}
					sq, ok := SquareFromCSA(tok[:2])
					if !ok || !sq.IsValid() {
						return nil, fmt.Errorf("csa-pos: bad PI square %q", tok[:2])
					}
					pos.Board[sq.Rank-1][sq.File-1] = Piece{}
				}
			}
		case len(line) >= 2 && line[0] == 'P' && line[1] >= '1' && line[1] <= '9':
			if pos == nil {
				pos = &Position{Ply: 1}
			}
			rankNum := int(line[1] - '0')
			if err := parseCSARankLine(pos, rankNum, line[2:]); err != nil {
				return nil, err
			}
		case strings.HasPrefix(line, "P+") || strings.HasPrefix(line, "P-"):
			if pos == nil {
				pos = &Position{Ply: 1}
			}
			color := Black
			if line[1] == '-' {
				color = White
			}
			rest := line[2:]
			// Format: repeats of <sq2><piece2> or "00<piece2>"; 00AL = "all remaining pieces".
			i := 0
			for i+4 <= len(rest) {
				tok := rest[i : i+4]
				i += 4
				if tok == "00AL" {
					return nil, fmt.Errorf("csa-pos: 00AL hand shorthand not supported")
				}
				if tok[:2] != "00" {
					return nil, fmt.Errorf("csa-pos: P%c hand entry must start with 00, got %q", line[1], tok)
				}
				pt, ok := pieceTypeFromCSA[tok[2:4]]
				if !ok || !pt.IsDroppable() {
					return nil, fmt.Errorf("csa-pos: bad hand piece code %q", tok[2:4])
				}
				pos.Hands[color][pt]++
			}
			if i != len(rest) {
				return nil, fmt.Errorf("csa-pos: trailing data in hand line %q", line)
			}
		case line == "+" || line == "-":
			if pos == nil {
				pos = NewStartPosition()
			}
			if line == "+" {
				pos.Turn = Black
			} else {
				pos.Turn = White
			}
			sideSet = true
		default:
			// Lines not matching known prefixes are ignored; CSA position
			// blocks can be embedded alongside other Game_Summary keys.
		}
	}

	if pos == nil {
		return nil, fmt.Errorf("csa-pos: empty or unrecognized input")
	}
	if !sideSet {
		// Default to Black; many Game_Summary blocks omit the line when
		// using PI because hirate implies Black-to-move.
		pos.Turn = Black
	}
	return pos, nil
}

// ParseCSAWithMoves parses a CSA position block, also extracting any CSA
// move lines ("+NNNNXX" / "-NNNNXX") that may be embedded for resumed
// games. The position returned is the block's declared starting point; the
// caller is expected to apply the returned moves in order to reach the
// current position.
func ParseCSAWithMoves(block string) (*Position, []string, error) {
	var moveBlock strings.Builder
	var moves []string
	for _, raw := range strings.Split(block, "\n") {
		line := strings.TrimSpace(raw)
		if len(line) == 7 && (line[0] == '+' || line[0] == '-') && isAllDigits(line[1:5]) {
			moves = append(moves, line)
			continue
		}
		moveBlock.WriteString(raw)
		moveBlock.WriteByte('\n')
	}
	pos, err := ParseCSAPosition(moveBlock.String())
	if err != nil {
		return nil, nil, err
	}
	return pos, moves, nil
}

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// parseCSARankLine fills pos.Board[rankNum-1][...] from a CSA rank body.
// The body is exactly 27 characters: 9 cells × 3 chars each ("+FU", "-HI",
// " * " for empty). Files are listed from 9 (left) down to 1 (right).
func parseCSARankLine(pos *Position, rankNum int, body string) error {
	if rankNum < 1 || rankNum > 9 {
		return fmt.Errorf("csa-pos: rank %d out of range", rankNum)
	}
	// Right-pad with spaces: real CSA streams and config files often drop
	// trailing whitespace, so the final cell of an empty-ending rank may
	// arrive as " *" instead of " * ".
	if len(body) < 27 {
		body = body + strings.Repeat(" ", 27-len(body))
	}
	body = body[:27]
	for idx := 0; idx < 9; idx++ {
		cell := body[idx*3 : idx*3+3]
		file := 9 - idx // leftmost cell = file 9
		switch cell[0] {
		case ' ':
			if cell != " * " && cell != "   " {
				return fmt.Errorf("csa-pos: P%d cell %q malformed", rankNum, cell)
			}
			pos.Board[rankNum-1][file-1] = Piece{}
		case '+', '-':
			color := Black
			if cell[0] == '-' {
				color = White
			}
			pt, ok := pieceTypeFromCSA[cell[1:3]]
			if !ok {
				return fmt.Errorf("csa-pos: P%d bad piece code %q", rankNum, cell[1:3])
			}
			pos.Board[rankNum-1][file-1] = Piece{Type: pt, Color: color}
		default:
			return fmt.Errorf("csa-pos: P%d cell %q bad leading char", rankNum, cell)
		}
	}
	return nil
}

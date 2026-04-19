package shogi

import (
	"fmt"
	"strconv"
	"strings"
)

// SFEN returns the position as a full SFEN string
// "<board> <side> <hands> <move>".
func (p *Position) SFEN() string {
	var sb strings.Builder

	// Board: ranks 1..9 top-down, separated by '/'.
	for r := 1; r <= 9; r++ {
		empty := 0
		// Within each rank, file 9 is leftmost → iterate file 9 down to 1.
		for f := 9; f >= 1; f-- {
			pc := p.Board[r-1][f-1]
			if pc.IsEmpty() {
				empty++
				continue
			}
			if empty > 0 {
				sb.WriteByte('0' + byte(empty))
				empty = 0
			}
			sb.WriteString(pc.SFENChar())
		}
		if empty > 0 {
			sb.WriteByte('0' + byte(empty))
		}
		if r < 9 {
			sb.WriteByte('/')
		}
	}

	sb.WriteByte(' ')
	if p.Turn == Black {
		sb.WriteByte('b')
	} else {
		sb.WriteByte('w')
	}

	sb.WriteByte(' ')
	sb.WriteString(p.sfenHands())

	sb.WriteByte(' ')
	sb.WriteString(strconv.Itoa(p.Ply))
	return sb.String()
}

// sfenHands emits hands in the conventional SFEN order: black pieces
// R,B,G,S,N,L,P (uppercase), then white r,b,g,s,n,l,p, with a digit prefix
// when count>1. Returns "-" if both hands are empty.
func (p *Position) sfenHands() string {
	var sb strings.Builder
	order := []PieceType{Rook, Bishop, Gold, Silver, Knight, Lance, Pawn}
	for _, c := range []Color{Black, White} {
		for _, pt := range order {
			n := p.Hands[c][pt]
			if n == 0 {
				continue
			}
			if n > 1 {
				sb.WriteString(strconv.Itoa(n))
			}
			ch := pieceTypeSFEN[pt]
			if c == White {
				ch = toLower(ch)
			}
			sb.WriteString(ch)
		}
	}
	if sb.Len() == 0 {
		return "-"
	}
	return sb.String()
}

// ParseSFEN parses a full SFEN string into a Position.
func ParseSFEN(s string) (*Position, error) {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) != 4 {
		return nil, fmt.Errorf("sfen: need 4 fields, got %d: %q", len(fields), s)
	}
	p := &Position{}

	rows := strings.Split(fields[0], "/")
	if len(rows) != 9 {
		return nil, fmt.Errorf("sfen: need 9 ranks, got %d", len(rows))
	}
	for ri, row := range rows {
		file := 9
		i := 0
		for i < len(row) {
			c := row[i]
			if c >= '1' && c <= '9' {
				file -= int(c - '0')
				i++
				continue
			}
			promoted := false
			if c == '+' {
				promoted = true
				i++
				if i >= len(row) {
					return nil, fmt.Errorf("sfen: trailing '+' in rank %d", ri+1)
				}
				c = row[i]
			}
			color := Black
			up := c
			if c >= 'a' && c <= 'z' {
				color = White
				up = c - ('a' - 'A')
			}
			pt, ok := pieceTypeFromSFEN[up]
			if !ok {
				return nil, fmt.Errorf("sfen: bad piece letter %q", string(c))
			}
			if promoted {
				pt = pt.Promoted()
			}
			if file < 1 || file > 9 {
				return nil, fmt.Errorf("sfen: rank %d file overflow", ri+1)
			}
			p.Board[ri][file-1] = Piece{Type: pt, Color: color}
			file--
			i++
		}
		if file != 0 {
			return nil, fmt.Errorf("sfen: rank %d underfilled (file=%d)", ri+1, file)
		}
	}

	switch fields[1] {
	case "b":
		p.Turn = Black
	case "w":
		p.Turn = White
	default:
		return nil, fmt.Errorf("sfen: bad side %q", fields[1])
	}

	if fields[2] != "-" {
		i := 0
		for i < len(fields[2]) {
			n := 1
			if fields[2][i] >= '0' && fields[2][i] <= '9' {
				j := i
				for j < len(fields[2]) && fields[2][j] >= '0' && fields[2][j] <= '9' {
					j++
				}
				v, err := strconv.Atoi(fields[2][i:j])
				if err != nil {
					return nil, fmt.Errorf("sfen: hand count: %w", err)
				}
				n = v
				i = j
			}
			if i >= len(fields[2]) {
				return nil, fmt.Errorf("sfen: hand letter missing after count")
			}
			c := fields[2][i]
			color := Black
			up := c
			if c >= 'a' && c <= 'z' {
				color = White
				up = c - ('a' - 'A')
			}
			pt, ok := pieceTypeFromSFEN[up]
			if !ok || !pt.IsDroppable() {
				return nil, fmt.Errorf("sfen: bad hand letter %q", string(c))
			}
			p.Hands[color][pt] += n
			i++
		}
	}

	ply, err := strconv.Atoi(fields[3])
	if err != nil {
		return nil, fmt.Errorf("sfen: bad move number %q", fields[3])
	}
	p.Ply = ply
	return p, nil
}

package shogi

// Square identifies a board square by file (1..9) and rank (1..9).
// File 1 is the rightmost column from Black's perspective. Rank 1 is the
// topmost row (White's back rank, = USI 'a'). Rank 9 is Black's back rank
// (= USI 'i'). The zero value is not a valid square.
type Square struct {
	File int // 1..9
	Rank int // 1..9; 1 = top (USI 'a'), 9 = bottom (USI 'i')
}

// IsValid reports whether both coordinates are in [1,9].
func (s Square) IsValid() bool {
	return s.File >= 1 && s.File <= 9 && s.Rank >= 1 && s.Rank <= 9
}

// USIRankLetter returns the USI rank character 'a'..'i'.
func (s Square) USIRankLetter() byte {
	return byte('a' + s.Rank - 1)
}

// SquareFromUSI parses two USI chars (file digit + rank letter), e.g. "7g".
// Returns ok=false on malformed input.
func SquareFromUSI(s string) (Square, bool) {
	if len(s) != 2 {
		return Square{}, false
	}
	f := int(s[0] - '0')
	r := int(s[1]-'a') + 1
	sq := Square{File: f, Rank: r}
	if !sq.IsValid() {
		return Square{}, false
	}
	return sq, true
}

// SquareFromCSA parses two CSA digits (file + rank), e.g. "77".
// "00" returns the zero Square and ok=true to signal a drop target marker.
func SquareFromCSA(s string) (Square, bool) {
	if len(s) != 2 {
		return Square{}, false
	}
	if s == "00" {
		return Square{}, true
	}
	f := int(s[0] - '0')
	r := int(s[1] - '0')
	sq := Square{File: f, Rank: r}
	if !sq.IsValid() {
		return Square{}, false
	}
	return sq, true
}

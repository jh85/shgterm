// Package shogi provides minimal shogi state: positions, moves, SFEN I/O,
// CSA position parsing, and CSA↔USI move conversion. It assumes all moves
// passed to Apply are legal — legality is the engine's responsibility.
package shogi

type Color uint8

const (
	Black Color = iota // 先手, first player (CSA '+')
	White              // 後手, second player (CSA '-')
)

// Opponent returns the other color.
func (c Color) Opponent() Color { return c ^ 1 }

// CSASign returns "+" for Black and "-" for White.
func (c Color) CSASign() byte {
	if c == Black {
		return '+'
	}
	return '-'
}

// PieceType covers all shogi piece kinds including promoted forms.
// The Empty value means "no piece".
type PieceType uint8

const (
	Empty PieceType = iota
	Pawn
	Lance
	Knight
	Silver
	Gold
	Bishop
	Rook
	King
	PPawn   // と
	PLance  // 成香 / 杏
	PKnight // 成桂 / 圭
	PSilver // 成銀 / 全
	PBishop // 馬
	PRook   // 龍
)

// IsPromoted reports whether pt is a promoted piece type.
func (pt PieceType) IsPromoted() bool { return pt >= PPawn }

// Promoted returns the promoted form. Gold/King/Empty return themselves.
// Already-promoted pieces return themselves.
func (pt PieceType) Promoted() PieceType {
	switch pt {
	case Pawn:
		return PPawn
	case Lance:
		return PLance
	case Knight:
		return PKnight
	case Silver:
		return PSilver
	case Bishop:
		return PBishop
	case Rook:
		return PRook
	}
	return pt
}

// Unpromoted returns the unpromoted base form. Non-promoted inputs return
// themselves.
func (pt PieceType) Unpromoted() PieceType {
	switch pt {
	case PPawn:
		return Pawn
	case PLance:
		return Lance
	case PKnight:
		return Knight
	case PSilver:
		return Silver
	case PBishop:
		return Bishop
	case PRook:
		return Rook
	}
	return pt
}

// IsDroppable reports whether a piece of this type can appear in a hand
// (i.e., is an unpromoted, non-King base piece).
func (pt PieceType) IsDroppable() bool {
	return pt >= Pawn && pt <= Rook
}

// Piece is a (type, color) pair. An Empty type means the square is vacant;
// the Color on an Empty piece is meaningless.
type Piece struct {
	Type  PieceType
	Color Color
}

// IsEmpty reports whether the piece represents an empty square.
func (p Piece) IsEmpty() bool { return p.Type == Empty }

// SFENChar returns the SFEN representation of p (e.g. "P", "+p", "K").
// Panics if p is empty.
func (p Piece) SFENChar() string {
	base := pieceTypeSFEN[p.Type.Unpromoted()]
	if p.Color == White {
		base = toLower(base)
	}
	if p.Type.IsPromoted() {
		return "+" + base
	}
	return base
}

// CSACode returns the 2-letter CSA piece code for p.Type (ignoring color;
// CSA encodes color separately as +/-). Panics if p is empty.
func (p Piece) CSACode() string { return pieceTypeCSA[p.Type] }

var pieceTypeSFEN = [...]string{
	Empty: "",
	Pawn:  "P", Lance: "L", Knight: "N", Silver: "S",
	Gold: "G", Bishop: "B", Rook: "R", King: "K",
}

var pieceTypeCSA = [...]string{
	Empty: "",
	Pawn:  "FU", Lance: "KY", Knight: "KE", Silver: "GI",
	Gold: "KI", Bishop: "KA", Rook: "HI", King: "OU",
	PPawn: "TO", PLance: "NY", PKnight: "NK", PSilver: "NG",
	PBishop: "UM", PRook: "RY",
}

// pieceTypeFromSFEN maps uppercase SFEN letters to unpromoted piece types.
var pieceTypeFromSFEN = map[byte]PieceType{
	'P': Pawn, 'L': Lance, 'N': Knight, 'S': Silver,
	'G': Gold, 'B': Bishop, 'R': Rook, 'K': King,
}

// pieceTypeFromCSA maps CSA 2-letter codes to piece types.
var pieceTypeFromCSA = map[string]PieceType{
	"FU": Pawn, "KY": Lance, "KE": Knight, "GI": Silver,
	"KI": Gold, "KA": Bishop, "HI": Rook, "OU": King,
	"TO": PPawn, "NY": PLance, "NK": PKnight, "NG": PSilver,
	"UM": PBishop, "RY": PRook,
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

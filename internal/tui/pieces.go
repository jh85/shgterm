package tui

import "github.com/jh85/shgterm/internal/shogi"

// KanjiGlyph returns the 1-char kanji glyph for a piece (empty string for
// shogi.Empty). All glyphs are East-Asian-Width Wide (2 cells).
func KanjiGlyph(p shogi.Piece) string {
	switch p.Type {
	case shogi.Pawn:
		return "歩"
	case shogi.Lance:
		return "香"
	case shogi.Knight:
		return "桂"
	case shogi.Silver:
		return "銀"
	case shogi.Gold:
		return "金"
	case shogi.Bishop:
		return "角"
	case shogi.Rook:
		return "飛"
	case shogi.King:
		return "玉" // Use 玉 uniformly; many UIs show 王 for Black, 玉 for White.
	case shogi.PPawn:
		return "と"
	case shogi.PLance:
		return "杏"
	case shogi.PKnight:
		return "圭"
	case shogi.PSilver:
		return "全"
	case shogi.PBishop:
		return "馬"
	case shogi.PRook:
		return "龍"
	}
	return ""
}

// ASCIIGlyph returns the 1- or 2-char USI notation for a piece, with case
// encoding color (uppercase=Black, lowercase=White). Promoted pieces are
// prefixed with '+'.
func ASCIIGlyph(p shogi.Piece) string {
	base := ""
	switch p.Type.Unpromoted() {
	case shogi.Pawn:
		base = "P"
	case shogi.Lance:
		base = "L"
	case shogi.Knight:
		base = "N"
	case shogi.Silver:
		base = "S"
	case shogi.Gold:
		base = "G"
	case shogi.Bishop:
		base = "B"
	case shogi.Rook:
		base = "R"
	case shogi.King:
		base = "K"
	default:
		return ""
	}
	if p.Color == shogi.White {
		base = string(base[0] + ('a' - 'A'))
	}
	if p.Type.IsPromoted() {
		return "+" + base
	}
	return base
}

// EmptyGlyph is the wide-width placeholder used for empty squares.
const EmptyGlyph = "・"

// HandOrder is the canonical display order of piece types in hands.
var HandOrder = []shogi.PieceType{
	shogi.Rook, shogi.Bishop, shogi.Gold, shogi.Silver,
	shogi.Knight, shogi.Lance, shogi.Pawn,
}

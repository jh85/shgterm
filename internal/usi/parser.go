// Package usi implements a minimal USI shogi engine driver: process
// lifecycle, handshake, go/bestmove, and info parsing. It is protocol-only
// and knows nothing about CSA or the bridge loop.
package usi

import (
	"fmt"
	"strconv"
	"strings"
)

// TimeControl is what the bridge hands to Go(). binc/winc are optional:
// if both are zero we emit "byoyomi <Byoyomi>", otherwise "binc ... winc ...".
type TimeControl struct {
	// Millisecond clocks for each side.
	BTime int64
	WTime int64
	// If BInc or WInc is non-zero, Fischer-style increment is used. Both
	// should be in milliseconds.
	BInc int64
	WInc int64
	// Byoyomi (millisecond). Used only when BInc == WInc == 0.
	Byoyomi int64
	// Infinite forces "go infinite" (ignores the numbers). Used rarely.
	Infinite bool
}

// FormatGo returns the USI "go" arguments for t (without the leading "go ").
func (t TimeControl) FormatGo() string {
	if t.Infinite {
		return "infinite"
	}
	head := fmt.Sprintf("btime %d wtime %d", t.BTime, t.WTime)
	if t.BInc != 0 || t.WInc != 0 {
		return head + fmt.Sprintf(" binc %d winc %d", t.BInc, t.WInc)
	}
	return head + fmt.Sprintf(" byoyomi %d", t.Byoyomi)
}

// FormatPosition returns a USI "position" command for the given SFEN start
// and subsequent USI moves. If startSFEN equals the hirate start SFEN, it
// emits "position startpos [moves ...]" as engines expect.
func FormatPosition(startSFEN string, moves []string) string {
	var sb strings.Builder
	if startSFEN == hirateSFEN {
		sb.WriteString("position startpos")
	} else {
		sb.WriteString("position sfen ")
		sb.WriteString(startSFEN)
	}
	if len(moves) > 0 {
		sb.WriteString(" moves")
		for _, m := range moves {
			sb.WriteByte(' ')
			sb.WriteString(m)
		}
	}
	return sb.String()
}

const hirateSFEN = "lnsgkgsnl/1r5b1/ppppppppp/9/9/9/PPPPPPPPP/1B5R1/LNSGKGSNL b - 1"

// Info is a parsed 'info ...' line. Fields that were not present in the
// line are left at their zero value; check Has* helpers where meaningful.
type Info struct {
	Depth    int
	SelDepth int
	MultiPV  int
	Nodes    int64
	NPS      int64
	TimeMS   int64
	HashFull int    // per-mille
	Score    int    // centipawns; see ScoreMate for mate distance
	ScoreCP  bool   // true if Score is a centipawn score
	ScoreMate int   // mate in N (signed); 0 means not a mate score
	LowerBound bool
	UpperBound bool
	PV       []string // USI move strings
	Raw      string   // the original line (for logging/comment passthrough)
}

// HasScore reports whether the info carried any score at all.
func (i Info) HasScore() bool { return i.ScoreCP || i.ScoreMate != 0 }

// ParseInfo parses a single "info ..." line. Returns an error if the line
// is not prefixed "info" or if a numeric token fails to parse.
func ParseInfo(line string) (Info, error) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "info") {
		return Info{}, fmt.Errorf("not an info line: %q", line)
	}
	info := Info{Raw: line}
	tokens := strings.Fields(line)
	// Skip "info".
	i := 1
	for i < len(tokens) {
		key := tokens[i]
		i++
		switch key {
		case "depth":
			if i >= len(tokens) {
				break
			}
			info.Depth, _ = strconv.Atoi(tokens[i])
			i++
		case "seldepth":
			if i >= len(tokens) {
				break
			}
			info.SelDepth, _ = strconv.Atoi(tokens[i])
			i++
		case "multipv":
			if i >= len(tokens) {
				break
			}
			info.MultiPV, _ = strconv.Atoi(tokens[i])
			i++
		case "nodes":
			if i >= len(tokens) {
				break
			}
			info.Nodes, _ = strconv.ParseInt(tokens[i], 10, 64)
			i++
		case "nps":
			if i >= len(tokens) {
				break
			}
			info.NPS, _ = strconv.ParseInt(tokens[i], 10, 64)
			i++
		case "time":
			if i >= len(tokens) {
				break
			}
			info.TimeMS, _ = strconv.ParseInt(tokens[i], 10, 64)
			i++
		case "hashfull":
			if i >= len(tokens) {
				break
			}
			info.HashFull, _ = strconv.Atoi(tokens[i])
			i++
		case "score":
			if i >= len(tokens) {
				break
			}
			kind := tokens[i]
			i++
			switch kind {
			case "cp":
				if i >= len(tokens) {
					break
				}
				v, _ := strconv.Atoi(tokens[i])
				info.Score = v
				info.ScoreCP = true
				i++
			case "mate":
				if i >= len(tokens) {
					break
				}
				// "mate +" / "mate -" (YaneuraOu extension) indicates sign
				// only; treat as large positive/negative mate distance.
				tok := tokens[i]
				if tok == "+" {
					info.ScoreMate = 1
				} else if tok == "-" {
					info.ScoreMate = -1
				} else {
					info.ScoreMate, _ = strconv.Atoi(tok)
					if info.ScoreMate == 0 {
						info.ScoreMate = 1 // assume +1 if engine sent "0"
					}
				}
				i++
			}
			// Optional trailing "lowerbound"/"upperbound".
			if i < len(tokens) {
				switch tokens[i] {
				case "lowerbound":
					info.LowerBound = true
					i++
				case "upperbound":
					info.UpperBound = true
					i++
				}
			}
		case "pv":
			// PV consumes all remaining tokens.
			info.PV = append([]string(nil), tokens[i:]...)
			i = len(tokens)
		case "string":
			// "info string ..." is free-form; consume the rest.
			i = len(tokens)
		case "currmove":
			if i < len(tokens) {
				i++
			}
		case "currmovenumber":
			if i < len(tokens) {
				i++
			}
		default:
			// Unknown key — skip one value token if present.
			if i < len(tokens) {
				i++
			}
		}
	}
	return info, nil
}

// BestMove is a parsed "bestmove <m> [ponder <p>]" line.
type BestMove struct {
	Move   string
	Ponder string
	Raw    string
}

// ParseBestMove parses a "bestmove ..." line.
func ParseBestMove(line string) (BestMove, error) {
	line = strings.TrimSpace(line)
	fs := strings.Fields(line)
	if len(fs) < 2 || fs[0] != "bestmove" {
		return BestMove{}, fmt.Errorf("not a bestmove line: %q", line)
	}
	bm := BestMove{Move: fs[1], Raw: line}
	for i := 2; i < len(fs); i++ {
		if fs[i] == "ponder" && i+1 < len(fs) {
			bm.Ponder = fs[i+1]
			i++
		}
	}
	return bm, nil
}

// Resign/win sentinel USI bestmove values.
const (
	BestMoveResign = "resign"
	BestMoveWin    = "win"
)

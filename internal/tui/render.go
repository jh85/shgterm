package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/jh85/shgterm/internal/csa"
	"github.com/jh85/shgterm/internal/shogi"
)

// Style constants. Keep them attribute-based (colors + Dim/Bold) so the
// TUI degrades gracefully on 16-color terminals.
var (
	styleDefault = tcell.StyleDefault
	styleTitle   = tcell.StyleDefault.Bold(true)
	styleBorder  = tcell.StyleDefault.Foreground(tcell.ColorGray)
	styleCoord   = tcell.StyleDefault.Foreground(tcell.ColorGray)
	// Black pieces = yellow; White pieces = default (white). Turn-of-side
	// styles preserve the color, just adding bold.
	styleBlack     = tcell.StyleDefault.Foreground(tcell.ColorYellow)
	styleWhite     = tcell.StyleDefault
	styleBlackTurn = tcell.StyleDefault.Foreground(tcell.ColorYellow).Bold(true)
	styleWhiteTurn = tcell.StyleDefault.Bold(true)
	styleEmpty     = tcell.StyleDefault.Foreground(tcell.ColorDarkGray)
	styleLastTo    = tcell.StyleDefault.Background(tcell.ColorDarkBlue)
	styleHandLbl   = tcell.StyleDefault.Foreground(tcell.ColorGray)
	styleConfirm   = tcell.StyleDefault.Foreground(tcell.ColorBlack).Background(tcell.ColorYellow).Bold(true)
	styleLogInfo   = tcell.StyleDefault.Foreground(tcell.ColorGray)
	styleLogWarn   = tcell.StyleDefault.Foreground(tcell.ColorYellow)
	styleLogErr    = tcell.StyleDefault.Foreground(tcell.ColorRed)
	styleLogDbg    = tcell.StyleDefault.Foreground(tcell.ColorDarkGray)
)

const (
	// Board pane geometry (left side).
	boardPaneX = 0
	boardPaneW = 42 // outer width including 1-col borders

	// Right pane starts at boardPaneW.
	rightPaneX = boardPaneW

	// Log pane height (inside the outer frame).
	logPaneH = 6
)

func (t *TUI) render() {
	t.screen.Clear()
	w, h := t.screen.Size()
	if w < 80 || h < 24 {
		t.renderCompact(w, h)
		t.screen.Show()
		return
	}

	// Outer frame.
	t.drawFrame(0, 0, w, h)
	// Title + status strip at the top of the inside.
	t.drawTitleBar(w)
	t.drawStatusBar(w)
	// Vertical split between board pane and right pane.
	split := boardPaneW
	if split > w-30 {
		split = w - 30
	}
	t.drawSplit(split, 3, h-logPaneH-1)
	// Board pane.
	t.drawBoardPane(1, 3)
	// Right pane.
	t.drawRightPane(split+1, 3, w-split-2)
	// Horizontal divider above log.
	t.drawLogDivider(split, h-logPaneH-1, w)
	// Log pane.
	t.drawLogPane(1, h-logPaneH, w-2, logPaneH-1)

	t.screen.Show()
}

func (t *TUI) drawFrame(x, y, w, h int) {
	t.screen.SetContent(x, y, '┌', nil, styleBorder)
	t.screen.SetContent(x+w-1, y, '┐', nil, styleBorder)
	t.screen.SetContent(x, y+h-1, '└', nil, styleBorder)
	t.screen.SetContent(x+w-1, y+h-1, '┘', nil, styleBorder)
	t.hfill(x+1, y, w-2, styleBorder, '─')
	t.hfill(x+1, y+h-1, w-2, styleBorder, '─')
	for r := 1; r < h-1; r++ {
		t.screen.SetContent(x, y+r, '│', nil, styleBorder)
		t.screen.SetContent(x+w-1, y+r, '│', nil, styleBorder)
	}
}

func (t *TUI) drawTitleBar(w int) {
	title := " shgterm "
	t.model.mu.Lock()
	confirming := t.model.ConfirmingQuit
	t.model.mu.Unlock()
	// Overlay the title onto the top border.
	t.putText(2, 0, styleTitle, title)
	hint := " q:quit  PgUp PgDn:log scroll "
	if confirming {
		hint = " quit? (y/N) "
	}
	style := styleCoord
	if confirming {
		style = styleConfirm
	}
	if x := w - 2 - asciiLen(hint); x > 2+asciiLen(title)+2 {
		t.putText(x, 0, style, hint)
	}
}

func (t *TUI) drawStatusBar(w int) {
	t.model.mu.Lock()
	defer t.model.mu.Unlock()
	eng := truncate(t.model.EngineName, 12)
	gameID := "-"
	startStr := "-"
	if s := t.model.Summary; s != nil {
		if s.ID != "" {
			gameID = truncate(s.ID, 16)
		}
	}
	if !t.model.GameStartedAt.IsZero() {
		startStr = t.model.GameStartedAt.Format("15:04:05")
	}
	line := fmt.Sprintf("engine: %-12s  game: %-16s  start: %-8s  state: %s",
		eng, gameID, startStr, t.gameStateLabel())
	row := 1
	t.clearRect(1, row, w-2, 1)
	t.putText(2, row, styleDefault, truncate(line, w-4))
	// Row 2 is a light divider inside the frame.
	t.hfill(1, 2, w-2, styleBorder, '─')
	t.screen.SetContent(0, 2, '├', nil, styleBorder)
	t.screen.SetContent(w-1, 2, '┤', nil, styleBorder)
}

func (t *TUI) gameStateLabel() string {
	if t.model.SessionFinished {
		if t.model.SessionErr != nil {
			return "session error"
		}
		if t.model.EndedResult != "" {
			base := t.model.EndedResult
			if t.model.EndedSpecial != "" {
				base = t.model.EndedResult + " (" + t.model.EndedSpecial + ")"
			}
			return "ended — " + base
		}
		return "ended"
	}
	if t.model.EndedResult != "" {
		if t.model.EndedSpecial != "" {
			return t.model.EndedResult + " (" + t.model.EndedSpecial + ")"
		}
		return t.model.EndedResult
	}
	if t.model.Summary == nil {
		return "connecting"
	}
	return "playing"
}

func (t *TUI) drawSplit(col, top, bottom int) {
	for r := top; r <= bottom; r++ {
		t.screen.SetContent(col, r, '│', nil, styleBorder)
	}
}

func (t *TUI) drawLogDivider(split, row, w int) {
	t.hfill(1, row, w-2, styleBorder, '─')
	t.screen.SetContent(0, row, '├', nil, styleBorder)
	t.screen.SetContent(w-1, row, '┤', nil, styleBorder)
	// Where the vertical split meets: ┴
	t.screen.SetContent(split, row, '┴', nil, styleBorder)
}

// ---- board pane -----------------------------------------------------------

func (t *TUI) drawBoardPane(x, y int) {
	t.model.mu.Lock()
	pos := t.model.Position
	flip := t.model.Flip
	ascii := t.model.Ascii
	lastCSA := t.model.LastMoveCSA
	t.model.mu.Unlock()

	// File-number header row at y.
	//   layout within the pane:
	//     x   x+1 x+2 x+3 x+4 x+5 x+6 ...
	//     "   a│ K1 K2 ..."
	// We set: left padding 3 cols for the rank column, then 2-space
	// offset, then 9 cells at 3 cols each.
	//
	// Coordinates in the pane (col offsets from x):
	//   0: (rank letter column space)
	//   1: (rank letter)
	//   2: '│' (left edge of board box)
	//   3: ' '  (leading pad inside box)
	//   4-5: first cell rune slot (wide)
	//   6: ' '
	//   7-8: second cell
	//   ...
	//   4 + 3*8 + 2 = 30: end of 9th cell
	//   31: ' '  (padding)  — actually we put no trailing pad; last cell ends at col 4+3*8+1=29
	//
	// For symmetry we place a trailing space then a '│' at col 31.
	// So inner width is cols 2..31 inclusive = 30 cells; but the box inner
	// between the │'s is 29 cells wide — fine, we just draw the right │ at 31.
	// The board box is 28 cells wide inside (1-col leading pad + 9 cells × 3
	// cols = 28), matching the board2.txt layout with symmetric 1-col
	// internal pads.
	leftEdge := x + 2
	rightEdge := x + 31
	// File-number header row:
	t.putText(x+1, y+0, styleCoord, " ") // placeholder over rank col
	for i := 0; i < 9; i++ {
		file := 9 - i
		if flip {
			file = i + 1
		}
		t.putText(x+5+3*i, y+0, styleCoord, fmt.Sprintf("%d", file))
	}

	// Inner top border of the board box.
	t.screen.SetContent(leftEdge, y+1, '┌', nil, styleBorder)
	t.screen.SetContent(rightEdge, y+1, '┐', nil, styleBorder)
	t.hfill(leftEdge+1, y+1, rightEdge-leftEdge-1, styleBorder, '─')

	// Rank rows (9 of them).
	for i := 0; i < 9; i++ {
		rankIdx := i // 0..8 top-to-bottom
		if flip {
			rankIdx = 8 - i
		}
		rankLetter := byte('a' + rankIdx)
		row := y + 2 + i
		t.screen.SetContent(x+1, row, rune(rankLetter), nil, styleCoord)
		t.screen.SetContent(leftEdge, row, '│', nil, styleBorder)
		t.screen.SetContent(rightEdge, row, '│', nil, styleBorder)
		for f := 0; f < 9; f++ {
			fileIdx := 8 - f // default: file 9 at f=0 → fileIdx=8 (board[rank][8])
			if flip {
				fileIdx = f
			}
			cellX := x + 4 + 3*f
			// Fill background first (1+2+empty pad = 3 cells wide).
			t.screen.SetContent(cellX-1, row, ' ', nil, styleDefault)
			t.screen.SetContent(cellX+2, row, ' ', nil, styleDefault)

			var glyph string
			var style tcell.Style
			if pos == nil {
				glyph = EmptyGlyph
				style = styleEmpty
			} else {
				pc := pos.Board[rankIdx][fileIdx]
				if pc.IsEmpty() {
					glyph = EmptyGlyph
					style = styleEmpty
				} else if ascii {
					glyph = ASCIIGlyph(pc)
					style = styleBlack
					if pc.Color == shogi.White {
						style = styleWhite
					}
				} else {
					glyph = KanjiGlyph(pc)
					style = styleBlack
					if pc.Color == shogi.White {
						style = styleWhite
					}
				}
			}
			// Highlight the to-square of the last move.
			if lastCSA != "" && matchesLastTo(lastCSA, rankIdx, fileIdx) {
				style = style.Background(tcell.ColorDarkBlue)
			}
			// Place the glyph (wide rune handles its own +2 advance via tcell).
			for _, r := range glyph {
				t.screen.SetContent(cellX, row, r, nil, style)
				cellX += runeWidth(r)
			}
		}
	}

	// Inner bottom border.
	t.screen.SetContent(leftEdge, y+11, '└', nil, styleBorder)
	t.screen.SetContent(rightEdge, y+11, '┘', nil, styleBorder)
	t.hfill(leftEdge+1, y+11, rightEdge-leftEdge-1, styleBorder, '─')

	// Hands (rows y+12 and y+13).
	t.drawHand(x+2, y+12, csa.Black)
	t.drawHand(x+2, y+13, csa.White)
}

// matchesLastTo reports whether (rankIdx, fileIdx) is the to-square of the
// given CSA move string (e.g. "+7776FU" or "+7776FU,T10").
func matchesLastTo(csaMove string, rankIdx, fileIdx int) bool {
	if len(csaMove) < 7 {
		return false
	}
	if csaMove[0] != '+' && csaMove[0] != '-' {
		return false
	}
	toFile := int(csaMove[3] - '0')
	toRank := int(csaMove[4] - '0')
	return (rankIdx+1) == toRank && (fileIdx+1) == toFile
}

func (t *TUI) drawHand(x, y int, color csa.PlayerColor) {
	t.model.mu.Lock()
	defer t.model.mu.Unlock()
	label := "先手: "
	labelStyle := styleBlack // Black = yellow per the user's color scheme
	if color == csa.White {
		label = "後手: "
		labelStyle = styleWhite
	}
	col := t.putRunes(x, y, labelStyle, label)
	if t.model.Position == nil {
		t.putRunes(col, y, styleEmpty, EmptyGlyph)
		return
	}
	shogiColor := shogi.Black
	if color == csa.White {
		shogiColor = shogi.White
	}
	any := false
	for _, pt := range HandOrder {
		n := t.model.Position.Hands[shogiColor][pt]
		if n == 0 {
			continue
		}
		any = true
		glyph := KanjiGlyph(shogi.Piece{Type: pt, Color: shogiColor})
		col = t.putRunes(col, y, labelStyle, glyph)
		if n > 1 {
			col = t.putText(col, y, labelStyle, fmt.Sprintf("x%d", n))
		}
		col = t.putText(col, y, styleDefault, " ")
	}
	if !any {
		t.putRunes(col, y, styleEmpty, EmptyGlyph)
	}
}

// ---- right pane ----------------------------------------------------------

func (t *TUI) drawRightPane(x, y, w int) {
	t.model.mu.Lock()
	summary := t.model.Summary
	pv := t.model.PV
	pos := t.model.Position
	cb, cw := t.model.ClockBlack, t.model.ClockWhite
	unit := t.model.TimeUnit
	turnSide := t.model.TurnSide
	turnStart := t.model.TurnStartedAt
	lastCSA := t.model.LastMoveCSA
	lastUSI := t.model.LastMoveUSI
	lastPly := t.model.LastPly
	ended := t.model.EndedResult
	endedSpecial := t.model.EndedSpecial
	t.model.mu.Unlock()

	row := y
	t.putText(x, row, styleTitle, "Clock")
	row++
	name := func(c csa.PlayerColor) string {
		if summary == nil {
			return "-"
		}
		n := summary.Players[c].Name
		if n == "" {
			return "-"
		}
		return n
	}
	// Remaining is the stored value (updated only at turn boundaries).
	// Consumed-this-turn only renders for the side currently on move.
	remaining := func(side csa.PlayerColor, stored int64) time.Duration {
		d := time.Duration(stored) * unit
		if d < 0 {
			d = 0
		}
		return d
	}
	consumed := func(side csa.PlayerColor) (time.Duration, bool) {
		if turnStart.IsZero() || side != turnSide {
			return 0, false
		}
		d := time.Since(turnStart)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	onMoveBlack := pos != nil && pos.Turn == shogi.Black
	onMoveWhite := pos != nil && pos.Turn == shogi.White
	prefix := func(onMove bool) string {
		if onMove {
			return "> "
		}
		return "  "
	}
	style := func(base tcell.Style, onMove bool) tcell.Style {
		if onMove {
			if base == styleBlack {
				return styleBlackTurn
			}
			return styleWhiteTurn
		}
		return base
	}
	row2 := func(label string, color csa.PlayerColor, onMove bool, base tcell.Style, stored int64) {
		line := fmt.Sprintf("%s%s %-12s  %s",
			prefix(onMove), label, truncate(name(color), 12), formatDur(remaining(color, stored)))
		if d, ok := consumed(color); ok {
			line += fmt.Sprintf("  (+%s)", formatDur(d))
		}
		t.putRunes(x, row, style(base, onMove), line)
	}
	// 先手 (Black, yellow).
	row2("先手", csa.Black, onMoveBlack, styleBlack, cb)
	row++
	// 後手 (White, default).
	row2("後手", csa.White, onMoveWhite, styleWhite, cw)
	row += 2

	t.putText(x, row, styleTitle, "Last move")
	row++
	if lastCSA != "" {
		t.putText(x, row, styleDefault, fmt.Sprintf("  %s  (%s)  ply=%d", lastCSA, lastUSI, lastPly))
	} else {
		t.putText(x, row, styleCoord, "  (none)")
	}
	row += 2

	t.putText(x, row, styleTitle, "Engine PV")
	row++
	if pv.Depth != 0 || pv.Nodes != 0 || len(pv.PV) > 0 {
		var score string
		if pv.ScoreMate != 0 {
			score = fmt.Sprintf("mate%+d", pv.ScoreMate)
		} else if pv.ScoreCP {
			score = fmt.Sprintf("%+dcp", pv.Score)
		}
		line := fmt.Sprintf("  d=%d %s %s nodes", pv.Depth, score, humanNum(pv.Nodes))
		if pv.NPS > 0 {
			line += fmt.Sprintf("  nps=%s", humanNum(pv.NPS))
		}
		t.putText(x, row, styleDefault, line)
		row++
		// Render up to 3 lines of PV (6 moves each).
		for i := 0; i < 3 && i*6 < len(pv.PV); i++ {
			upper := (i + 1) * 6
			if upper > len(pv.PV) {
				upper = len(pv.PV)
			}
			seg := strings.Join(pv.PV[i*6:upper], " ")
			t.putText(x, row, styleDefault, "  "+truncate(seg, w-2))
			row++
		}
	} else {
		t.putText(x, row, styleCoord, "  (no analysis yet)")
		row++
	}
	row++

	t.putText(x, row, styleTitle, "Time control")
	row++
	if summary != nil {
		tc := summary.Players[csa.Black].Time
		totalDur := time.Duration(tc.TotalTimeUnits) * unit
		byoDur := time.Duration(tc.ByoyomiUnits) * unit
		incDur := time.Duration(tc.IncrementUnits) * unit
		// These lines mix wide kanji (2 cells each) with ASCII — use
		// putRunes so each wide rune occupies 2 grid cells correctly.
		t.putRunes(x, row, styleDefault, fmt.Sprintf("  持ち時間: %s", formatDur(totalDur)))
		row++
		t.putRunes(x, row, styleDefault, fmt.Sprintf("  秒読み:   %s", formatSeconds(byoDur)))
		row++
		t.putRunes(x, row, styleDefault, fmt.Sprintf("  加算:     %s", formatSeconds(incDur)))
		row++
	}
	if ended != "" {
		row++
		label := ended
		if endedSpecial != "" {
			label = ended + " (" + endedSpecial + ")"
		}
		t.putText(x, row, styleLogWarn, "  "+label)
	}
}

// ---- log pane ------------------------------------------------------------

func (t *TUI) drawLogPane(x, y, w, h int) {
	t.model.mu.Lock()
	defer t.model.mu.Unlock()
	entries := t.model.LogRing
	scroll := t.model.LogScroll
	tail := len(entries) - scroll
	head := tail - h
	if head < 0 {
		head = 0
	}
	row := y
	for i := head; i < tail && row < y+h; i++ {
		e := entries[i]
		var style tcell.Style
		level := "INFO "
		switch e.Level {
		case LogWarn:
			style = styleLogWarn
			level = "WARN "
		case LogErr:
			style = styleLogErr
			level = "ERROR"
		case LogDbg:
			style = styleLogDbg
			level = "DEBUG"
		default:
			style = styleLogInfo
		}
		line := fmt.Sprintf("%s %s  %s", e.Time.Format("15:04:05"), level, e.Msg)
		t.putText(x, row, style, truncate(line, w))
		row++
	}
}

// ---- fallback -----------------------------------------------------------

func (t *TUI) renderCompact(w, h int) {
	_ = h
	t.putText(0, 0, styleLogWarn, truncate("shgterm: terminal too small; resize to at least 80×24", w))
	t.model.mu.Lock()
	defer t.model.mu.Unlock()
	row := 1
	if t.model.Summary != nil {
		t.putText(0, row, styleDefault, truncate("game="+t.model.Summary.ID, w))
		row++
	}
	if t.model.LastMoveCSA != "" {
		t.putText(0, row, styleDefault, truncate("last="+t.model.LastMoveCSA, w))
	}
}

// ---- misc helpers ---------------------------------------------------------

func asciiLen(s string) int { return len(s) }

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if asciiLen(s) <= w {
		return s
	}
	if w <= 1 {
		return s[:1]
	}
	return s[:w-1] + "…"
}

func humanNum(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	}
	return fmt.Sprintf("%d", n)
}

func runeWidth(r rune) int {
	if isWide(r) {
		return 2
	}
	return 1
}

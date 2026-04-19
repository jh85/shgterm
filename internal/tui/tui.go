// Package tui implements the tcell-based board/PV/clock/log terminal UI.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/jh85/shgterm/internal/csa"
	"github.com/jh85/shgterm/internal/shogi"
	"github.com/jh85/shgterm/internal/usi"
)

// TUI owns the tcell screen and the model. It satisfies bridge.UI.
type TUI struct {
	screen tcell.Screen
	model  *Model

	cancel context.CancelFunc
	dirty  chan struct{}
}

// New creates a TUI but does not yet take over the terminal. Call Run to
// enter the alt screen. cancel is invoked when the user quits; the
// bridge's ctx should be derived from the same cancel so quitting cleanly
// ends the session.
func New(cancel context.CancelFunc, ascii, flip bool) (*TUI, error) {
	s, err := tcell.NewScreen()
	if err != nil {
		return nil, err
	}
	return newWithScreen(s, cancel, ascii, flip), nil
}

// newWithScreen wraps an already-built tcell.Screen. Used by tests with a
// SimulationScreen so rendering can be exercised without a real terminal.
func newWithScreen(s tcell.Screen, cancel context.CancelFunc, ascii, flip bool) *TUI {
	m := NewModel()
	m.Ascii = ascii
	m.Flip = flip
	return &TUI{
		screen: s,
		model:  m,
		cancel: cancel,
		dirty:  make(chan struct{}, 1),
	}
}

// Run initializes the screen and runs the event loop until ctx is done
// (bridge finishing) or the user quits.
func (t *TUI) Run(ctx context.Context) error {
	if err := t.screen.Init(); err != nil {
		return err
	}
	defer t.screen.Fini()
	t.screen.SetStyle(tcell.StyleDefault)
	t.screen.Clear()

	// Dispatch tcell events to a channel.
	evCh := make(chan tcell.Event, 16)
	go func() {
		for {
			ev := t.screen.PollEvent()
			if ev == nil {
				return
			}
			select {
			case evCh <- ev:
			default:
			}
		}
	}()

	// 1 Hz is plenty for a clock countdown and keeps CPU negligible
	// (tcell only pushes changed cells).
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	t.render()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.dirty:
			t.render()
		case <-ticker.C:
			// Force periodic redraw to refresh timestamps etc.
			t.render()
		case ev := <-evCh:
			if t.handleEvent(ev) {
				t.cancel()
				return nil
			}
			t.render()
		}
	}
}

// handleEvent returns true when the user wants to quit. 'q' enters a
// confirmation mode; only a subsequent 'y' actually quits. Ctrl-C bypasses
// the confirmation as an unconditional escape hatch.
func (t *TUI) handleEvent(ev tcell.Event) bool {
	switch e := ev.(type) {
	case *tcell.EventKey:
		// In confirmation mode, consume the next key.
		if t.model.ConfirmingQuit {
			if e.Rune() == 'y' || e.Rune() == 'Y' {
				return true
			}
			t.clearConfirmQuit()
			return false
		}
		switch e.Key() {
		case tcell.KeyCtrlC:
			return true
		case tcell.KeyEscape:
			// Swallow; Esc shouldn't quit silently once the UI is live.
			return false
		case tcell.KeyPgUp:
			t.scrollLog(+10)
		case tcell.KeyPgDn:
			t.scrollLog(-10)
		case tcell.KeyUp:
			t.scrollLog(+1)
		case tcell.KeyDown:
			t.scrollLog(-1)
		case tcell.KeyEnd:
			t.setLogScroll(0)
		case tcell.KeyHome:
			t.setLogScroll(1 << 30)
		default:
			if e.Rune() == 'q' || e.Rune() == 'Q' {
				t.setConfirmQuit()
			}
		}
	case *tcell.EventResize:
		t.screen.Sync()
	}
	return false
}

func (t *TUI) setConfirmQuit() {
	t.model.mu.Lock()
	t.model.ConfirmingQuit = true
	t.model.mu.Unlock()
}

func (t *TUI) clearConfirmQuit() {
	t.model.mu.Lock()
	t.model.ConfirmingQuit = false
	t.model.mu.Unlock()
}

func (t *TUI) scrollLog(delta int) {
	t.model.mu.Lock()
	defer t.model.mu.Unlock()
	t.model.LogScroll += delta
	if t.model.LogScroll < 0 {
		t.model.LogScroll = 0
	}
	if t.model.LogScroll > len(t.model.LogRing) {
		t.model.LogScroll = len(t.model.LogRing)
	}
}

func (t *TUI) setLogScroll(v int) {
	t.model.mu.Lock()
	defer t.model.mu.Unlock()
	if v > len(t.model.LogRing) {
		v = len(t.model.LogRing)
	}
	if v < 0 {
		v = 0
	}
	t.model.LogScroll = v
}

func (t *TUI) markDirty() {
	select {
	case t.dirty <- struct{}{}:
	default:
	}
}

// ---- bridge.UI implementation -------------------------------------------

func (t *TUI) SetEngine(name, author string) {
	t.model.mu.Lock()
	t.model.EngineName = name
	t.model.EngineAuthor = author
	t.model.mu.Unlock()
	t.markDirty()
}

func (t *TUI) SetGame(summary *csa.GameSummary, my csa.PlayerColor) {
	t.model.mu.Lock()
	t.model.Summary = summary
	t.model.MyColor = my
	// Flip is controlled only by the --flip CLI flag. By default the
	// board is rendered in the standard orientation with Black at the
	// bottom regardless of which side the engine plays.
	t.model.EndedResult = ""
	t.model.EndedSpecial = ""
	t.model.LastMoveCSA = ""
	t.model.LastMoveUSI = ""
	t.model.TurnStartedAt = time.Time{}
	t.model.mu.Unlock()
	t.markDirty()
}

func (t *TUI) SetPosition(pos *shogi.Position, lastCSA, lastUSI string) {
	t.model.mu.Lock()
	t.model.Position = pos
	t.model.LastMoveCSA = lastCSA
	t.model.LastMoveUSI = lastUSI
	if pos != nil {
		t.model.LastPly = pos.Ply - 1
	}
	t.model.mu.Unlock()
	t.markDirty()
}

func (t *TUI) SetClock(b, w int64, unit time.Duration) {
	t.model.mu.Lock()
	t.model.ClockBlack = b
	t.model.ClockWhite = w
	t.model.TimeUnit = unit
	t.model.mu.Unlock()
	t.markDirty()
}

func (t *TUI) SetPV(info usi.Info) {
	t.model.mu.Lock()
	t.model.PV = info
	t.model.mu.Unlock()
	t.markDirty()
}

func (t *TUI) LogLine(level, msg string) {
	l := LogInfo
	switch strings.ToLower(level) {
	case "warn":
		l = LogWarn
	case "err", "error":
		l = LogErr
	case "debug", "dbg":
		l = LogDbg
	}
	t.model.AppendLog(l, msg)
	t.markDirty()
}

func (t *TUI) GameEnded(result, special string) {
	t.model.mu.Lock()
	t.model.EndedResult = result
	t.model.EndedSpecial = special
	t.model.TurnStartedAt = time.Time{}
	t.model.mu.Unlock()
	t.markDirty()
}

// SetTurnTimer records that side's clock has just begun running at
// startedAt. Passing a zero time.Time stops the countdown.
func (t *TUI) SetTurnTimer(side csa.PlayerColor, startedAt time.Time) {
	t.model.mu.Lock()
	t.model.TurnSide = side
	t.model.TurnStartedAt = startedAt
	t.model.mu.Unlock()
	t.markDirty()
}

// SessionEnded is called by main.go after bridge.Run returns. The TUI keeps
// running so the user can review final state; status is updated to reflect
// the session being over.
func (t *TUI) SessionEnded(err error) {
	t.model.mu.Lock()
	t.model.SessionFinished = true
	t.model.SessionErr = err
	t.model.TurnStartedAt = time.Time{}
	t.model.mu.Unlock()
	if err != nil {
		t.LogLine("warn", fmt.Sprintf("session ended: %v", err))
	} else {
		t.LogLine("info", "session ended; press q to quit")
	}
}

// ---- helpers -------------------------------------------------------------

// putText draws a plain ASCII/narrow-rune string starting at (col,row). It
// does not attempt east-asian-width handling — use putRunes for CJK.
func (t *TUI) putText(col, row int, style tcell.Style, s string) int {
	for _, r := range s {
		t.screen.SetContent(col, row, r, nil, style)
		col++
	}
	return col
}

// putRunes draws a string, advancing by 2 columns per wide rune. Returns
// the next free column.
func (t *TUI) putRunes(col, row int, style tcell.Style, s string) int {
	for _, r := range s {
		w := 1
		if isWide(r) {
			w = 2
		}
		t.screen.SetContent(col, row, r, nil, style)
		col += w
	}
	return col
}

// isWide reports whether r is East-Asian Wide/Fullwidth.
func isWide(r rune) bool {
	// Tcell ships its own runewidth handling; we use a coarse check that
	// matches what tcell will do, to keep our layout math simple.
	switch {
	case r >= 0x1100 && r <= 0x115F: // Hangul Jamo
		return true
	case r >= 0x2E80 && r <= 0x9FFF: // CJK Unified
		return true
	case r >= 0xAC00 && r <= 0xD7A3: // Hangul Syllables
		return true
	case r >= 0xF900 && r <= 0xFAFF: // CJK Compat
		return true
	case r >= 0xFE30 && r <= 0xFE4F: // CJK Compat Forms
		return true
	case r >= 0xFF00 && r <= 0xFF60, r >= 0xFFE0 && r <= 0xFFE6: // Fullwidth forms
		return true
	case r >= 0x3000 && r <= 0x303E: // CJK Symbols (incl. `・`? `・` is 0x30FB)
		return true
	case r >= 0x30A0 && r <= 0x30FF: // Katakana (incl. 0x30FB `・`)
		return true
	case r >= 0x3040 && r <= 0x309F: // Hiragana
		return true
	}
	return false
}

// hfill draws a horizontal run of rune r from col..col+n-1 at row.
func (t *TUI) hfill(col, row, n int, style tcell.Style, r rune) {
	for i := 0; i < n; i++ {
		t.screen.SetContent(col+i, row, r, nil, style)
	}
}

// clearRect blanks cells in the given rectangle with the default style.
func (t *TUI) clearRect(x, y, w, h int) {
	for r := 0; r < h; r++ {
		for c := 0; c < w; c++ {
			t.screen.SetContent(x+c, y+r, ' ', nil, tcell.StyleDefault)
		}
	}
}

// formatClock renders a clock value (in time units) as "MM:SS" (or
// "HH:MM:SS" when >1h). Negative becomes "00:00".
func formatClock(units int64, unit time.Duration) string {
	return formatDur(time.Duration(units) * unit)
}

// formatDur renders a duration as "MM:SS" or "HH:MM:SS". Negative → 00:00.
func formatDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	s := int((d % time.Minute) / time.Second)
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// formatExtra renders byoyomi/increment hint, e.g. "+ 00:10" (byoyomi) or "+ +5s".
func formatExtra(s *csa.GameSummary, color csa.PlayerColor) string {
	if s == nil {
		return ""
	}
	tc := s.Players[color].Time
	unit := tc.TimeUnit
	if unit == 0 {
		unit = time.Second
	}
	inc := time.Duration(tc.IncrementUnits) * unit
	if inc > 0 {
		return fmt.Sprintf("+%ds", int(inc/time.Second))
	}
	by := time.Duration(tc.ByoyomiUnits) * unit
	if by > 0 {
		return fmt.Sprintf("+ %s", formatClock(tc.ByoyomiUnits, unit))
	}
	return ""
}

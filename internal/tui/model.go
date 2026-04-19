package tui

import (
	"sync"
	"time"

	"github.com/jh85/shgterm/internal/csa"
	"github.com/jh85/shgterm/internal/shogi"
	"github.com/jh85/shgterm/internal/usi"
)

// LogLevel enumerates log message severities for colorization.
type LogLevel uint8

const (
	LogInfo LogLevel = iota
	LogWarn
	LogErr
	LogDbg
)

// LogEntry is one line in the log pane.
type LogEntry struct {
	Time  time.Time
	Level LogLevel
	Msg   string
}

// Model holds all TUI state. All accesses go through the lock.
type Model struct {
	mu sync.Mutex

	// Engine identity (set once after handshake).
	EngineName, EngineAuthor string

	// Latest game summary from the server.
	Summary *csa.GameSummary
	MyColor csa.PlayerColor

	// GameStartedAt is the wall-clock time we first learned about the
	// current game (i.e., Game_Summary arrival). Used for the "start:"
	// field in the status bar.
	GameStartedAt time.Time

	// Position snapshot + last move.
	Position    *shogi.Position
	LastMoveCSA string
	LastMoveUSI string
	LastPly     int

	// Clocks in time units + unit.
	ClockBlack, ClockWhite int64
	TimeUnit               time.Duration

	// Live-countdown support. When TurnStartedAt is non-zero, the side
	// TurnSide's displayed clock is reduced by (now - TurnStartedAt).
	TurnSide      csa.PlayerColor
	TurnStartedAt time.Time

	// Latest engine PV info.
	PV usi.Info

	// Final game state (for rendering the "ended" banner).
	EndedResult  string
	EndedSpecial string

	// SessionFinished becomes true once bridge.Run returns; the TUI then
	// keeps running but the status bar shows "session ended".
	SessionFinished bool
	SessionErr      error

	// ConfirmingQuit: true while waiting for the y/N response after 'q'.
	ConfirmingQuit bool

	// Log ring (most-recent at tail).
	LogRing []LogEntry
	LogMax  int
	// LogScroll counts entries from the tail; 0 means auto-tail.
	LogScroll int

	// Flip reports whether the board should be shown from the opponent's
	// perspective (i.e., we are White and prefer our side at the bottom).
	Flip bool

	// Ascii toggles USI letters instead of kanji.
	Ascii bool
}

// NewModel returns a Model with default capacities.
func NewModel() *Model {
	return &Model{LogMax: 500, TimeUnit: time.Second}
}

// WithLock runs fn with the model locked. Convenience for readers/writers.
func (m *Model) WithLock(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn()
}

// AppendLog adds an entry and trims the ring.
func (m *Model) AppendLog(level LogLevel, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.LogRing = append(m.LogRing, LogEntry{Time: time.Now(), Level: level, Msg: msg})
	if len(m.LogRing) > m.LogMax {
		m.LogRing = m.LogRing[len(m.LogRing)-m.LogMax:]
	}
}

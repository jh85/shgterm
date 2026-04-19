package bridge

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/jh85/shgterm/internal/csa"
	"github.com/jh85/shgterm/internal/shogi"
	"github.com/jh85/shgterm/internal/usi"
)

// UI receives updates from the bridge loop. All methods must be safe to
// call from the bridge goroutine; implementations are responsible for
// thread-safety across their own rendering.
type UI interface {
	// Engine info (after handshake).
	SetEngine(name, author string)
	// Game metadata arrived from CSA Game_Summary.
	SetGame(summary *csa.GameSummary, myColor csa.PlayerColor)
	// Called after every applied move.
	SetPosition(pos *shogi.Position, lastMoveCSA, lastMoveUSI string)
	// Clock update in time units; callers render them with SetTimeUnit.
	SetClock(blackUnits, whiteUnits int64, unit time.Duration)
	// SetTurnTimer reports that the given side's clock has just begun
	// running at startedAt (wall clock). Pass a zero time.Time to stop the
	// live countdown.
	SetTurnTimer(side csa.PlayerColor, startedAt time.Time)
	// Engine PV/score update during its turn.
	SetPV(info usi.Info)
	// Free-text log line (protocol I/O summary, state transitions, etc.).
	LogLine(level, msg string)
	// GameEnded is called once per game with the CSA result code ("#WIN", ...).
	GameEnded(result, special string)
	// SessionEnded is called once main.go observes bridge.Run returning.
	// The UI may keep rendering; shgterm stays up until the user quits.
	SessionEnded(err error)
}

// StderrUI is a zero-dependency UI that writes updates to an io.Writer
// (typically os.Stderr). Used when --no-tui is set.
type StderrUI struct {
	mu sync.Mutex
	w  io.Writer
}

// NewStderrUI returns a StderrUI writing to w.
func NewStderrUI(w io.Writer) *StderrUI { return &StderrUI{w: w} }

func (u *StderrUI) println(format string, args ...any) {
	u.mu.Lock()
	defer u.mu.Unlock()
	fmt.Fprintf(u.w, format+"\n", args...)
}

func (u *StderrUI) SetEngine(name, author string) {
	u.println("[engine] %s  by %s", name, author)
}

func (u *StderrUI) SetGame(s *csa.GameSummary, my csa.PlayerColor) {
	if s == nil {
		return
	}
	side := "+"
	if my == csa.White {
		side = "-"
	}
	u.println("[game]   id=%s  side=%s  %s(+)  vs  %s(-)  byoyomi=%ds  total=%d×%v",
		s.ID, side, s.Players[csa.Black].Name, s.Players[csa.White].Name,
		s.Players[my].Time.ByoyomiUnits, s.Players[my].Time.TotalTimeUnits,
		s.Players[my].Time.TimeUnit)
}

func (u *StderrUI) SetPosition(p *shogi.Position, csaMove, usiMove string) {
	if csaMove == "" {
		return
	}
	u.println("[move]   %s  (%s)  ply=%d", csaMove, usiMove, p.Ply-1)
}

func (u *StderrUI) SetClock(b, w int64, unit time.Duration) {
	u.println("[clock]  black=%v  white=%v", time.Duration(b)*unit, time.Duration(w)*unit)
}

func (u *StderrUI) SetPV(info usi.Info) {
	score := ""
	if info.ScoreMate != 0 {
		score = fmt.Sprintf("mate%+d ", info.ScoreMate)
	} else if info.ScoreCP {
		score = fmt.Sprintf("%+dcp ", info.Score)
	}
	u.println("[pv]     d=%d %snodes=%d pv=%v", info.Depth, score, info.Nodes, info.PV)
}

func (u *StderrUI) LogLine(level, msg string) {
	u.println("[%s] %s", level, msg)
}

func (u *StderrUI) GameEnded(result, special string) {
	if special != "" {
		u.println("[end]    %s (%s)", result, special)
	} else {
		u.println("[end]    %s", result)
	}
}

func (u *StderrUI) SetTurnTimer(side csa.PlayerColor, startedAt time.Time) {
	// Stderr UI is static-log-only: no live countdown to render.
	_ = side
	_ = startedAt
}

func (u *StderrUI) SessionEnded(err error) {
	if err != nil {
		u.println("[done]   %v", err)
	} else {
		u.println("[done]   session finished")
	}
}

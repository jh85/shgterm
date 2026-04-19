// Package bridge wires a USI engine to a CSA server: the per-game state
// machine, clock bookkeeping, move conversion, and kifu saving.
package bridge

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jh85/shgterm/internal/config"
	"github.com/jh85/shgterm/internal/csa"
	"github.com/jh85/shgterm/internal/kifu"
	"github.com/jh85/shgterm/internal/shogi"
	"github.com/jh85/shgterm/internal/usi"
)

// Logger is the shared logger interface; same shape as csa/usi loggers.
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Err(msg string, args ...any)
}

type nopLogger struct{}

func (nopLogger) Info(string, ...any) {}
func (nopLogger) Warn(string, ...any) {}
func (nopLogger) Err(string, ...any)  {}

// Options passed to Run.
type Options struct {
	Config    *config.Config
	UI        UI
	Logger    Logger
	RecordDir string
	// LoginRetryDelay is used when AutoRelogin is on. Default 30s.
	LoginRetryDelay time.Duration
}

// Run is the main entry point. It blocks until the configured number of
// games complete, the context is cancelled, or an unrecoverable error
// occurs. Engine is started once; CSA is dialed per session.
func Run(ctx context.Context, opts Options) error {
	if opts.Config == nil {
		return errors.New("bridge: nil config")
	}
	if opts.UI == nil {
		opts.UI = NewStderrUI(nopWriter{})
	}
	if opts.Logger == nil {
		opts.Logger = nopLogger{}
	}
	if opts.LoginRetryDelay == 0 {
		opts.LoginRetryDelay = 30 * time.Second
	}
	cfg := opts.Config

	engine := usi.New(usi.Options{
		Path:   cfg.USI.Path,
		Name:   cfg.USI.Name,
		Logger: engineLogger{opts.Logger},
	})
	if err := engine.Start(ctx); err != nil {
		return fmt.Errorf("start engine: %w", err)
	}
	defer func() {
		quitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = engine.Quit(quitCtx)
	}()

	if err := engine.Handshake(ctx, usiOptionMap(cfg.USI.Options)); err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	opts.UI.SetEngine(engine.IDName(), engine.IDAuthor())

	remaining := cfg.Repeat
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		played, err := runOneSession(ctx, opts, engine, remaining)
		remaining -= played
		if err != nil {
			opts.Logger.Warn("session error: %v", err)
			opts.UI.LogLine("warn", fmt.Sprintf("session error: %v", err))
			if !cfg.AutoRelogin || remaining <= 0 {
				return err
			}
			opts.UI.LogLine("info", fmt.Sprintf("retrying in %v", opts.LoginRetryDelay))
			select {
			case <-time.After(opts.LoginRetryDelay):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
	}
	return nil
}

// runOneSession logs in, plays up to 'budget' games, then logs out. Returns
// the number of games actually played and the terminating error (if any).
func runOneSession(ctx context.Context, opts Options, engine *usi.Engine, budget int) (played int, err error) {
	cfg := opts.Config
	client := csa.New(csa.Options{
		Host:        cfg.Server.Host,
		Port:        cfg.Server.Port,
		ID:          cfg.Server.ID,
		Password:    cfg.Server.Password,
		Protocol:    csa.Protocol(cfg.Server.ProtocolVersion),
		Keepalive:   time.Duration(cfg.Server.TCPKeepalive.InitialDelay) * time.Second,
		DialTimeout: 30 * time.Second,
		Logger:      csaLogger{opts.Logger},
	})
	if bp := cfg.Server.BlankLinePing; bp != nil {
		client.SetBlankLinePing(
			time.Duration(bp.InitialDelay)*time.Second,
			time.Duration(bp.Interval)*time.Second,
		)
	}
	if err := client.Dial(ctx); err != nil {
		return 0, err
	}
	defer client.Close()

	for played < budget {
		if err := ctx.Err(); err != nil {
			return played, err
		}
		summary, err := waitForGameSummary(ctx, client)
		if err != nil {
			return played, err
		}
		res, err := playOneGame(ctx, opts, engine, client, summary)
		if err != nil {
			return played, err
		}
		played++
		opts.UI.GameEnded(res.result, res.special)
		if cfg.SaveRecordFile {
			if err := saveRecord(opts.RecordDir, cfg, summary, res); err != nil {
				opts.Logger.Warn("save record: %v", err)
				opts.UI.LogLine("warn", fmt.Sprintf("save record: %v", err))
			}
		}
	}
	if err := client.Logout(); err != nil {
		opts.Logger.Warn("logout: %v", err)
	}
	return played, nil
}

// waitForGameSummary waits for LOGIN_OK (once) then EventGameSummary.
// Intermediate events are forwarded to the log.
func waitForGameSummary(ctx context.Context, client *csa.Client) (*csa.GameSummary, error) {
	for {
		select {
		case ev, ok := <-client.Events():
			if !ok {
				return nil, errors.New("csa stream closed")
			}
			switch ev.Kind {
			case csa.EventGameSummary:
				return ev.Summary, nil
			case csa.EventLoginFailed:
				return nil, ev.Err
			case csa.EventError:
				return nil, ev.Err
			case csa.EventClosed:
				return nil, errors.New("csa closed before Game_Summary")
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

type gameResult struct {
	result      string // "#WIN"/"#LOSE"/"#DRAW"/"#CENSORED"/"#CHUDAN"
	special     string // "#RESIGN" / "#TIME_UP" / ...
	moveLines   []string
	terminator  string
	startedAt   time.Time
	endedAt     time.Time
	initialPos  string
	summary     *csa.GameSummary
}

// playOneGame drives one agreed game end-to-end. Returns once the server
// sends a result code.
func playOneGame(ctx context.Context, opts Options, engine *usi.Engine, client *csa.Client, summary *csa.GameSummary) (gameResult, error) {
	res := gameResult{startedAt: time.Now(), initialPos: summary.Position, summary: summary}

	pos, preMoves, err := shogi.ParseCSAWithMoves(summary.Position)
	if err != nil {
		return res, fmt.Errorf("parse position: %w", err)
	}
	// Apply any pre-moves recorded in the position block (resumed games).
	usiMoves := make([]string, 0, 256)
	for _, ml := range preMoves {
		m, err := pos.CSAToMove(ml)
		if err != nil {
			return res, fmt.Errorf("pre-move %q: %w", ml, err)
		}
		if err := pos.Apply(m); err != nil {
			return res, fmt.Errorf("apply pre-move: %w", err)
		}
		usiMoves = append(usiMoves, m.FormatUSI())
	}

	opts.UI.SetGame(summary, summary.MyColor)
	opts.UI.SetPosition(pos, "", "")

	// Initial clocks: TotalTime + one Increment (per CSA convention).
	clock := [2]int64{}
	for i := range clock {
		clock[i] = summary.Players[i].Time.TotalTimeUnits + summary.Players[i].Time.IncrementUnits
	}
	unit := summary.Players[0].Time.TimeUnit
	if unit == 0 {
		unit = time.Second
	}
	opts.UI.SetClock(clock[csa.Black], clock[csa.White], unit)

	if err := engine.NewGame(); err != nil {
		return res, fmt.Errorf("usinewgame: %w", err)
	}

	// AGREE and wait for START.
	if err := client.Agree(summary.ID); err != nil {
		return res, err
	}
	if err := waitForStart(ctx, client); err != nil {
		return res, err
	}

	// The clock for whoever is on move starts running now.
	opts.UI.SetTurnTimer(colorToCSA(pos.Turn), time.Now())

	// Main alternating loop.
	for {
		if pos.Turn == colorToShogi(summary.MyColor) {
			// Our turn: ask the engine for a move.
			decision, err := engineThink(ctx, engine, pos, usiMoves, summary, clock, opts.UI)
			if err != nil {
				return res, err
			}
			switch {
			case decision.declareWin:
				if err := client.DeclareWin(); err != nil {
					return res, err
				}
				res.terminator = "%KACHI"
			case decision.resign:
				if err := client.Resign(); err != nil {
					return res, err
				}
				res.terminator = "%TORYO"
			default:
				csaLine, err := pos.MoveToCSA(*decision.move, pos.Turn)
				if err != nil {
					return res, fmt.Errorf("convert bestmove: %w", err)
				}
				comment := ""
				if opts.Config.EnableComment && opts.Config.IsFloodgateProtocol() {
					comment = buildComment(decision.info)
				}
				if err := client.SendMove(csaLine, comment); err != nil {
					return res, err
				}
				// Apply locally on server echo — do nothing here.
			}
		}

		// Await next event from server (our echo, opponent, or end).
		done, err := awaitMoveOrEnd(ctx, client, pos, &usiMoves, &res, clock, unit, opts.UI)
		if err != nil {
			return res, err
		}
		if done {
			res.endedAt = time.Now()
			_ = engine.Gameover(mapGameResultForEngine(res.result, summary.MyColor))
			return res, nil
		}
	}
}

// waitForStart drains events until EventStart or a terminal condition.
func waitForStart(ctx context.Context, client *csa.Client) error {
	for {
		select {
		case ev, ok := <-client.Events():
			if !ok {
				return errors.New("csa closed before START")
			}
			switch ev.Kind {
			case csa.EventStart:
				return nil
			case csa.EventRejectedByServer:
				return errors.New("server REJECTed our AGREE")
			case csa.EventError:
				return ev.Err
			case csa.EventClosed:
				return errors.New("csa closed before START")
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// engineDecision is engineThink's result. Exactly one of move/resign/
// declareWin is set when err == nil.
type engineDecision struct {
	move       *shogi.Move
	resign     bool
	declareWin bool
	info       *usi.Info
}

// engineThink asks the engine for a move. It maps USI "bestmove resign"
// and empty bestmove to resign=true, and USI "bestmove win" to
// declareWin=true (to be answered with CSA %KACHI).
func engineThink(
	ctx context.Context,
	engine *usi.Engine,
	pos *shogi.Position,
	usiMoves []string,
	summary *csa.GameSummary,
	clock [2]int64,
	ui UI,
) (engineDecision, error) {
	positionCmd := usi.FormatPosition(startSFENFromSummary(summary), usiMoves)
	tc := buildTimeControl(summary, clock)
	ch, err := engine.Go(ctx, positionCmd, tc)
	if err != nil {
		return engineDecision{}, err
	}

	var lastInfo *usi.Info
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return engineDecision{info: lastInfo}, errors.New("engine closed channel without bestmove")
			}
			if ev.Error != nil {
				return engineDecision{info: lastInfo}, ev.Error
			}
			if ev.Info != nil {
				lastInfo = ev.Info
				ui.SetPV(*ev.Info)
				continue
			}
			if ev.BestMove != nil {
				switch ev.BestMove.Move {
				case usi.BestMoveResign, "":
					return engineDecision{resign: true, info: lastInfo}, nil
				case usi.BestMoveWin:
					return engineDecision{declareWin: true, info: lastInfo}, nil
				}
				m, perr := shogi.ParseUSIMove(ev.BestMove.Move)
				if perr != nil {
					return engineDecision{info: lastInfo}, fmt.Errorf("parse engine move %q: %w", ev.BestMove.Move, perr)
				}
				return engineDecision{move: &m, info: lastInfo}, nil
			}
		case <-ctx.Done():
			_ = engine.Stop()
			// drain until bestmove or error
			for ev := range ch {
				_ = ev
			}
			return engineDecision{info: lastInfo}, ctx.Err()
		}
	}
}

// awaitMoveOrEnd blocks until a move echo or a game-ending event. When a
// move arrives (ours or opponent's), it applies it to pos, appends USI
// string, updates clock, and returns (false, nil). On game end, returns
// (true, nil) with res populated.
func awaitMoveOrEnd(
	ctx context.Context,
	client *csa.Client,
	pos *shogi.Position,
	usiMoves *[]string,
	res *gameResult,
	clock [2]int64,
	unit time.Duration,
	ui UI,
) (bool, error) {
	for {
		select {
		case ev, ok := <-client.Events():
			if !ok {
				return true, errors.New("csa stream closed during game")
			}
			switch ev.Kind {
			case csa.EventMove:
				// Apply to local Position.
				base := stripTime(ev.Move)
				m, err := pos.CSAToMove(base)
				if err != nil {
					return true, fmt.Errorf("csa→move: %w", err)
				}
				usiStr := m.FormatUSI()
				if err := pos.Apply(m); err != nil {
					return true, fmt.Errorf("apply move: %w", err)
				}
				*usiMoves = append(*usiMoves, usiStr)
				res.moveLines = append(res.moveLines, ev.Move)
				// Update clock: subtract elapsed from the mover.
				mover := ev.Color
				clock[mover] -= ev.ElapsedUnits
				if clock[mover] < 0 {
					clock[mover] = 0
				}
				clock[mover] += res.summary.Players[mover].Time.IncrementUnits
				ui.SetPosition(pos, ev.Move, usiStr)
				ui.SetClock(clock[csa.Black], clock[csa.White], unit)
				// Whose turn just started — its clock is now running.
				ui.SetTurnTimer(colorToCSA(pos.Turn), time.Now())
				return false, nil
			case csa.EventSpecialMove:
				res.special = ev.Special
				// terminator lines follow — append for kifu
				res.moveLines = append(res.moveLines, ev.Special)
			case csa.EventResult:
				res.result = ev.Result
				ui.SetTurnTimer(0, time.Time{}) // stop the countdown
				return true, nil
			case csa.EventError:
				return true, ev.Err
			case csa.EventClosed:
				return true, errors.New("csa closed during game")
			}
		case <-ctx.Done():
			return true, ctx.Err()
		}
	}
}

// ---- helpers -------------------------------------------------------------

func colorToShogi(c csa.PlayerColor) shogi.Color {
	if c == csa.White {
		return shogi.White
	}
	return shogi.Black
}

func colorToCSA(c shogi.Color) csa.PlayerColor {
	if c == shogi.White {
		return csa.White
	}
	return csa.Black
}

// mapGameResultForEngine turns a CSA result into a USI gameover argument
// from the engine's perspective.
func mapGameResultForEngine(csaResult string, my csa.PlayerColor) string {
	switch csaResult {
	case "#WIN":
		return "win"
	case "#LOSE":
		return "lose"
	default:
		return "draw"
	}
}

func usiOptionMap(opts map[string]config.USIOption) map[string]string {
	out := make(map[string]string, len(opts))
	for name, o := range opts {
		switch v := o.Value.(type) {
		case bool:
			if v {
				out[name] = "true"
			} else {
				out[name] = "false"
			}
		case int:
			out[name] = strconv.Itoa(v)
		case int64:
			out[name] = strconv.FormatInt(v, 10)
		case float64:
			// YAML numbers come in as float64 by default.
			if o.Type == "spin" || o.Type == "check" {
				out[name] = strconv.FormatInt(int64(v), 10)
			} else {
				out[name] = strconv.FormatFloat(v, 'f', -1, 64)
			}
		case string:
			out[name] = v
		default:
			out[name] = fmt.Sprint(v)
		}
	}
	return out
}

// startSFENFromSummary parses the CSA initial position to derive a starting
// SFEN the engine will accept. Returns the hirate SFEN on parse failure.
func startSFENFromSummary(s *csa.GameSummary) string {
	p, err := shogi.ParseCSAPosition(s.Position)
	if err != nil {
		return "lnsgkgsnl/1r5b1/ppppppppp/9/9/9/PPPPPPPPP/1B5R1/LNSGKGSNL b - 1"
	}
	return p.SFEN()
}

// buildTimeControl maps a CSA time config to USI time options.
func buildTimeControl(s *csa.GameSummary, clock [2]int64) usi.TimeControl {
	unit := s.Players[csa.Black].Time.TimeUnit
	if unit == 0 {
		unit = time.Second
	}
	ms := func(units int64) int64 { return int64(time.Duration(units) * unit / time.Millisecond) }
	tc := usi.TimeControl{
		BTime: ms(clock[csa.Black]),
		WTime: ms(clock[csa.White]),
	}
	if s.Players[csa.Black].Time.IncrementUnits > 0 || s.Players[csa.White].Time.IncrementUnits > 0 {
		tc.BInc = ms(s.Players[csa.Black].Time.IncrementUnits)
		tc.WInc = ms(s.Players[csa.White].Time.IncrementUnits)
	} else {
		tc.Byoyomi = ms(s.Players[csa.Black].Time.ByoyomiUnits)
	}
	return tc
}

// buildComment produces a Floodgate "* <score> <pv>" body from the last info.
func buildComment(info *usi.Info) string {
	if info == nil {
		return ""
	}
	var score string
	if info.ScoreMate != 0 {
		// Large surrogate score for mate.
		sign := 1
		if info.ScoreMate < 0 {
			sign = -1
		}
		score = strconv.Itoa(sign * 30000)
	} else if info.ScoreCP {
		score = strconv.Itoa(info.Score)
	} else {
		return ""
	}
	pv := strings.Join(info.PV, " ")
	if pv == "" {
		return "* " + score
	}
	return "* " + score + " " + pv
}

var timeTailRe = regexp.MustCompile(`,T[0-9]+$`)

func stripTime(line string) string {
	return timeTailRe.ReplaceAllString(line, "")
}

func saveRecord(dir string, cfg *config.Config, s *csa.GameSummary, r gameResult) error {
	rec := kifu.Record{
		GameID:        s.ID,
		BlackName:     s.Players[csa.Black].Name,
		WhiteName:     s.Players[csa.White].Name,
		StartedAt:     r.startedAt,
		EndedAt:       r.endedAt,
		InitialCSAPos: r.initialPos,
		MoveLines:     r.moveLines,
		Terminator:    r.result,
	}
	unit := s.Players[csa.Black].Time.TimeUnit
	if unit == 0 {
		unit = time.Second
	}
	rec.TimeLimitSec = int64(time.Duration(s.Players[csa.Black].Time.TotalTimeUnits) * unit / time.Second)
	rec.ByoyomiSec = int64(time.Duration(s.Players[csa.Black].Time.ByoyomiUnits) * unit / time.Second)
	path, err := kifu.Write(dir, cfg.RecordFileNameTemplate, rec)
	if err != nil {
		return err
	}
	_ = path
	return nil
}

// --- small adapters -------------------------------------------------------

type engineLogger struct{ inner Logger }

func (l engineLogger) Info(m string, a ...any) { l.inner.Info(m, a...) }
func (l engineLogger) Warn(m string, a ...any) { l.inner.Warn(m, a...) }
func (l engineLogger) Err(m string, a ...any)  { l.inner.Err(m, a...) }

type csaLogger struct{ inner Logger }

func (l csaLogger) Info(m string, a ...any) { l.inner.Info(m, a...) }
func (l csaLogger) Warn(m string, a ...any) { l.inner.Warn(m, a...) }
func (l csaLogger) Err(m string, a ...any)  { l.inner.Err(m, a...) }

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

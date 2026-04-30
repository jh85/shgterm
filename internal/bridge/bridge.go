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
	// HandshakeTimeout / ReadyTimeout override the corresponding usi
	// defaults. Zero means "use the usi package default".
	HandshakeTimeout time.Duration
	ReadyTimeout     time.Duration
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

	engine, err := startAndHandshake(ctx, cfg, opts.Logger, opts.HandshakeTimeout, opts.ReadyTimeout)
	if err != nil {
		return err
	}
	// Use a closure-captured variable so the defer always sees the most
	// recent engine (a per-game restart may replace it).
	defer func() {
		quitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = engine.Quit(quitCtx)
	}()
	opts.UI.SetEngine(engine.IDName(), engine.IDAuthor())

	infinite := cfg.Repeat == -1
	remaining := cfg.Repeat
	// gameCount persists across sessions so the "game N started/ended"
	// log lines keep incrementing even when the CSA server drops us
	// between games (common on v121 tournament servers).
	gameCount := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !infinite && remaining <= 0 {
			return nil
		}
		budget := remaining
		if infinite {
			budget = -1
		}
		played, err := runOneSession(ctx, opts, &engine, budget, &gameCount)
		if !infinite {
			remaining -= played
		}
		if err != nil {
			opts.Logger.Warn("session error: %v", err)
			if !cfg.AutoRelogin {
				opts.UI.LogLine("warn", fmt.Sprintf("session error: %v", err))
				return err
			}
			if !infinite && remaining <= 0 {
				opts.UI.LogLine("warn", fmt.Sprintf("session error: %v", err))
				return err
			}
			// A post-game disconnect is normal on v121 tournament servers;
			// demote the user-visible message and just announce the retry.
			opts.UI.LogLine("info",
				fmt.Sprintf("reconnecting in %v (press q to quit)", opts.LoginRetryDelay))
			select {
			case <-time.After(opts.LoginRetryDelay):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		// Normal return only happens when budget is reached; infinite
		// mode only returns here if ctx was cancelled (checked at top).
	}
}

// runOneSession logs in, plays up to 'budget' games, then logs out. Returns
// the number of games actually played and the terminating error (if any).
// enginePtr is a pointer to the current engine pointer so the per-game
// restart path can replace it in place. gameCount is a caller-owned
// cross-session counter used only for labeling log lines.
func runOneSession(ctx context.Context, opts Options, enginePtr **usi.Engine, budget int, gameCount *int) (played int, err error) {
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

	infinite := budget < 0
	for {
		if err := ctx.Err(); err != nil {
			return played, err
		}
		if !infinite && played >= budget {
			break
		}
		// Between games (Floodgate schedules roughly every 30 minutes;
		// tournaments can be hours apart). We stay logged in and block
		// on the next Game_Summary. TCP keepalive + optional blank-line
		// ping keep the connection alive while we wait.
		if played > 0 {
			opts.UI.LogLine("info", "waiting for next game…")
		}
		summary, err := waitForGameSummary(ctx, client)
		if err != nil {
			return played, err
		}

		res, err := playOneGame(ctx, opts, *enginePtr, client, summary, gameCount)
		if err != nil {
			return played, err
		}
		if res.rejected {
			// Server cancelled the match before it could start (the
			// opponent did not AGREE in time). The connection is still
			// alive; just loop back and wait for the next proposal —
			// no game number burned, no record saved.
			opts.UI.LogLine("warn", fmt.Sprintf("match cancelled by server (id=%s) — opponent did not AGREE",
				truncateRunes(summary.ID, 10)))
			continue
		}
		played++
		opts.UI.GameEnded(res.result, res.special)
		endLabel := res.result
		if res.special != "" {
			endLabel = res.result + " (" + res.special + ")"
		}
		opts.UI.LogLine("info", fmt.Sprintf("game %d ended: %s", res.gameNumber, endLabel))
		if cfg.SaveRecordFile {
			if err := saveRecord(opts.RecordDir, cfg, summary, res); err != nil {
				opts.Logger.Warn("save record: %v", err)
				opts.UI.LogLine("warn", fmt.Sprintf("save record: %v", err))
			}
		}

		// Restart the engine between games if configured. Unlike
		// ShogiHome we do NOT reconnect to CSA — keeping the login alive
		// is important for Floodgate, which pushes successive Game_Summary
		// blocks on the same session. Skip restart before the final game
		// of a finite budget to avoid wasting the teardown+NN-load cost.
		if cfg.RestartPlayerEveryGame {
			last := !infinite && played >= budget
			if !last {
				opts.UI.LogLine("info", "restarting engine (restartPlayerEveryGame=true)…")
				newEngine, err := restartEngine(ctx, *enginePtr, cfg, opts.Logger, opts.HandshakeTimeout, opts.ReadyTimeout)
				if err != nil {
					return played, fmt.Errorf("restart engine: %w", err)
				}
				*enginePtr = newEngine
				opts.UI.SetEngine(newEngine.IDName(), newEngine.IDAuthor())
				opts.UI.LogLine("info", fmt.Sprintf("engine restarted: %s", newEngine.IDName()))
			}
		}
	}
	if err := client.Logout(); err != nil {
		opts.Logger.Warn("logout: %v", err)
	}
	return played, nil
}

// startAndHandshake spawns the engine process and performs the full USI
// handshake (usi → usiok → setoption… → isready → readyok). On any error
// after Start, the partially-started process is torn down.
func startAndHandshake(ctx context.Context, cfg *config.Config, log Logger, handshakeTimeout, readyTimeout time.Duration) (*usi.Engine, error) {
	engine := usi.New(usi.Options{
		Path:             cfg.USI.Path,
		Name:             cfg.USI.Name,
		Logger:           engineLogger{log},
		HandshakeTimeout: handshakeTimeout,
		ReadyTimeout:     readyTimeout,
	})
	if err := engine.Start(ctx); err != nil {
		return nil, fmt.Errorf("start engine: %w", err)
	}
	if err := engine.Handshake(ctx, usiOptionList(cfg.USI.Options)); err != nil {
		quitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = engine.Quit(quitCtx)
		return nil, fmt.Errorf("handshake: %w", err)
	}
	return engine, nil
}

// restartEngine tears down the supplied engine and replaces it with a
// freshly-started one. The old engine is always torn down before we try
// to build a new one so we don't end up with two live NN workers.
func restartEngine(ctx context.Context, old *usi.Engine, cfg *config.Config, log Logger, handshakeTimeout, readyTimeout time.Duration) (*usi.Engine, error) {
	quitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := old.Quit(quitCtx); err != nil {
		log.Warn("old engine quit: %v", err)
	}
	return startAndHandshake(ctx, cfg, log, handshakeTimeout, readyTimeout)
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
	result         string // "#WIN"/"#LOSE"/"#DRAW"/"#CENSORED"/"#CHUDAN"
	special        string // "#RESIGN" / "#TIME_UP" / ...
	moveLines      []string
	moveTimestamps []time.Time // wall-clock time each EventMove was received
	terminator     string
	startedAt      time.Time
	endedAt        time.Time
	initialPos     string
	summary        *csa.GameSummary
	gameNumber     int  // 1-based, set when START actually fires
	rejected       bool // server REJECTed our AGREE; no game was played
}

// playOneGame drives one game end-to-end. It sends AGREE, awaits START,
// and runs the move loop until the server emits a result code. If the
// server REJECTs the AGREE (e.g. the opponent never agreed), it returns
// with res.rejected = true and the connection is left open. gameCount is
// incremented only when START actually fires, so cancelled proposals
// don't burn a game number.
func playOneGame(ctx context.Context, opts Options, engine *usi.Engine, client *csa.Client, summary *csa.GameSummary, gameCount *int) (gameResult, error) {
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
	rejected, err := waitForStart(ctx, client)
	if err != nil {
		return res, err
	}
	if rejected {
		// The match was cancelled by the server before the game could
		// start (typically because the opponent did not AGREE within
		// the server's deadline). The CSA session is still alive — the
		// caller can loop back to wait for the next proposal.
		res.rejected = true
		return res, nil
	}

	// START fired — this is now a real, played game. Burn a game number
	// and emit the start log line.
	*gameCount++
	res.gameNumber = *gameCount
	blackName := summary.Players[csa.Black].Name
	whiteName := summary.Players[csa.White].Name
	if blackName == "" {
		blackName = "-"
	}
	if whiteName == "" {
		whiteName = "-"
	}
	opts.UI.LogLine("info", fmt.Sprintf("game %d started: ☗ %s  ☖ %s (id=%s)",
		res.gameNumber, blackName, whiteName, truncateRunes(summary.ID, 10)))

	// The clock for whoever is on move starts running now.
	opts.UI.SetTurnTimer(colorToCSA(pos.Turn), time.Now())

	// Main alternating loop.
	for {
		if pos.Turn == colorToShogi(summary.MyColor) {
			// Our turn: ask the engine for a move. engineThink also
			// watches the CSA stream for a game-end event so we don't
			// miss e.g. #SENNICHITE/#DRAW arriving while the engine is
			// still thinking.
			decision, err := engineThink(ctx, engine, client, pos, usiMoves, summary, clock, opts.UI)
			if err != nil {
				return res, err
			}
			if decision.gameEnded {
				// Record what engineThink already consumed from the CSA
				// stream; awaitMoveOrEnd below will pick up whatever
				// termination event is still pending (SpecialMove or
				// Result — whichever hasn't arrived yet).
				if decision.csaSpecialMove != "" {
					res.special = decision.csaSpecialMove
					res.moveLines = append(res.moveLines, decision.csaSpecialMove)
				}
				if decision.csaResult != "" {
					res.result = decision.csaResult
					res.endedAt = time.Now()
					_ = engine.Gameover(mapGameResultForEngine(res.result, summary.MyColor))
					return res, nil
				}
				// Fall through to awaitMoveOrEnd for the pending Result.
			} else {
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
		}

		// Await next event from server (our echo, opponent, or end).
		done, err := awaitMoveOrEnd(ctx, client, pos, &usiMoves, &res, &clock, unit, opts.UI)
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

// waitForStart drains events until EventStart, EventRejectedByServer, or
// a terminal condition. It distinguishes a server REJECT (the opponent
// did not AGREE in time, no game was played) from a real error: the
// caller can keep the connection alive and wait for the next proposal.
func waitForStart(ctx context.Context, client *csa.Client) (rejected bool, err error) {
	for {
		select {
		case ev, ok := <-client.Events():
			if !ok {
				return false, errors.New("csa closed before START")
			}
			switch ev.Kind {
			case csa.EventStart:
				return false, nil
			case csa.EventRejectedByServer:
				return true, nil
			case csa.EventError:
				return false, ev.Err
			case csa.EventClosed:
				return false, errors.New("csa closed before START")
			}
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

// engineDecision is engineThink's result. When err == nil exactly one of
// move / resign / declareWin / gameEnded is set. gameEnded signals that a
// game-terminating CSA event (EventSpecialMove or EventResult) arrived
// while the engine was thinking; caller must NOT try to SendMove and
// should fold csaSpecialMove / csaResult into the gameResult.
type engineDecision struct {
	move       *shogi.Move
	resign     bool
	declareWin bool
	info       *usi.Info

	gameEnded      bool
	csaSpecialMove string // "#SENNICHITE", "#TIME_UP", etc. — may be empty
	csaResult      string // "#WIN"/"#LOSE"/"#DRAW"/"#CENSORED"/"#CHUDAN" — may be empty
}

// engineThink asks the engine for a move while also watching the CSA
// stream for a game-end event. If the server sends SENNICHITE / a result
// code / a connection close while we're waiting, we Stop() the engine,
// drain its output, and return a gameEnded decision so the caller can
// finalize the game cleanly (no SendMove race against StateWaitingGameSummary).
//
// USI "bestmove resign"/empty maps to resign=true; "bestmove win" maps to
// declareWin=true (answered with CSA %KACHI).
func engineThink(
	ctx context.Context,
	engine *usi.Engine,
	client *csa.Client,
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
	csaCh := client.Events()

	// abort stops the engine and drains its output channel to the close
	// that follows the bestmove triggered by Stop().
	abort := func() {
		_ = engine.Stop()
		for range ch {
		}
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
		case csaEv, ok := <-csaCh:
			if !ok {
				abort()
				return engineDecision{info: lastInfo}, errors.New("csa stream closed during engine think")
			}
			switch csaEv.Kind {
			case csa.EventSpecialMove:
				// Record and abort; the Result line will arrive next and
				// awaitMoveOrEnd picks it up after we return.
				abort()
				return engineDecision{
					gameEnded:      true,
					csaSpecialMove: csaEv.Special,
					info:           lastInfo,
				}, nil
			case csa.EventResult:
				abort()
				return engineDecision{
					gameEnded: true,
					csaResult: csaEv.Result,
					info:      lastInfo,
				}, nil
			case csa.EventError:
				abort()
				return engineDecision{info: lastInfo}, csaEv.Err
			case csa.EventClosed:
				abort()
				return engineDecision{info: lastInfo}, errors.New("csa closed during engine think")
			case csa.EventMove:
				// Shouldn't happen while it's our turn, but log and keep
				// thinking rather than silently dropping it.
				ui.LogLine("warn", fmt.Sprintf("unexpected CSA move during engine think: %q", csaEv.Move))
			}
		case <-ctx.Done():
			abort()
			return engineDecision{info: lastInfo}, ctx.Err()
		}
	}
}

// awaitMoveOrEnd blocks until a move echo or a game-ending event. When a
// move arrives (ours or opponent's), it applies it to pos, appends USI
// string, updates clock (in place via the pointer — the caller relies on
// the mutation), and returns (false, nil). On game end returns (true, nil)
// with res populated.
func awaitMoveOrEnd(
	ctx context.Context,
	client *csa.Client,
	pos *shogi.Position,
	usiMoves *[]string,
	res *gameResult,
	clock *[2]int64,
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
				res.moveTimestamps = append(res.moveTimestamps, time.Now())
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

// usiOptionList converts a config.USIOptions block (which preserves
// document order) into the ordered slice that engine.Handshake expects.
// Some engines require setoptions in a specific sequence; the user's YAML
// order is the contract.
func usiOptionList(opts config.USIOptions) []usi.Setoption {
	out := make([]usi.Setoption, 0, opts.Len())
	for _, name := range opts.Order {
		o := opts.Map[name]
		var val string
		switch v := o.Value.(type) {
		case bool:
			if v {
				val = "true"
			} else {
				val = "false"
			}
		case int:
			val = strconv.Itoa(v)
		case int64:
			val = strconv.FormatInt(v, 10)
		case float64:
			if o.Type == "spin" || o.Type == "check" {
				val = strconv.FormatInt(int64(v), 10)
			} else {
				val = strconv.FormatFloat(v, 'f', -1, 64)
			}
		case string:
			val = v
		default:
			val = fmt.Sprint(v)
		}
		out = append(out, usi.Setoption{Name: name, Value: val})
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

// truncateRunes returns the first n runes of s, preserving UTF-8 integrity.
// (Plain s[:n] slicing could cut a multi-byte sequence in half.)
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}

func saveRecord(dir string, cfg *config.Config, s *csa.GameSummary, r gameResult) error {
	rec := kifu.Record{
		GameID:         s.ID,
		BlackName:      s.Players[csa.Black].Name,
		WhiteName:      s.Players[csa.White].Name,
		StartedAt:      r.startedAt,
		EndedAt:        r.endedAt,
		InitialCSAPos:  r.initialPos,
		MoveLines:      r.moveLines,
		MoveTimestamps: r.moveTimestamps,
		Terminator:     r.result,
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

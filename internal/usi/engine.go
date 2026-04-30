package usi

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Logger is the minimal interface the USI client uses for diagnostics.
// Info = normal protocol I/O, Warn = recoverable oddities, Err = failures.
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Err(msg string, args ...any)
}

type nopLogger struct{}

func (nopLogger) Info(string, ...any) {}
func (nopLogger) Warn(string, ...any) {}
func (nopLogger) Err(string, ...any)  {}

// Options configure Engine construction.
type Options struct {
	Path             string
	Args             []string
	Name             string        // for logging; defaults to basename(Path)
	Logger           Logger        // defaults to no-op
	HandshakeTimeout time.Duration // default 30s
	ReadyTimeout     time.Duration // default 60s
	QuitTimeout      time.Duration // default 5s
}

// EngineOption mirrors one "option name ..." line from the engine.
type EngineOption struct {
	Name, Type, Default, Min, Max string
	Vars                          []string
}

// Event is what Go() delivers: an info update, a terminal bestmove, or a
// reader error. The channel is closed after a BestMove or Error.
type Event struct {
	Info     *Info
	BestMove *BestMove
	Error    error
}

// Engine wraps one running USI engine subprocess.
type Engine struct {
	opts Options
	cmd  *exec.Cmd
	in   io.WriteCloser

	writeMu sync.Mutex // serializes stdin writes

	mu        sync.Mutex
	idName    string
	idAuthor  string
	engineOpt map[string]EngineOption
	goCh      chan<- Event // set while a Go() is in-flight
	usiOKCh   chan struct{}
	readyCh   chan struct{}
	closed    bool

	readDone chan struct{}
	readErr  error
}

// New creates (but does not start) an Engine.
func New(opts Options) *Engine {
	if opts.Logger == nil {
		opts.Logger = nopLogger{}
	}
	if opts.HandshakeTimeout == 0 {
		opts.HandshakeTimeout = 60 * time.Second
	}
	if opts.ReadyTimeout == 0 {
		// NN engines (dlshogi-family, ONNX, CUDA init, mmap of large hash
		// tables) can legitimately take a couple of minutes on first
		// isready. 3 minutes matches shogihome's engine-timeout default.
		opts.ReadyTimeout = 3 * time.Minute
	}
	if opts.QuitTimeout == 0 {
		opts.QuitTimeout = 5 * time.Second
	}
	if opts.Name == "" {
		opts.Name = filepath.Base(opts.Path)
	}
	return &Engine{
		opts:      opts,
		engineOpt: map[string]EngineOption{},
		usiOKCh:   make(chan struct{}, 1),
		readyCh:   make(chan struct{}, 1),
	}
}

// Start launches the subprocess and starts the stdout reader.
func (e *Engine) Start(ctx context.Context) error {
	e.cmd = exec.CommandContext(ctx, e.opts.Path, e.opts.Args...)
	e.cmd.Dir = filepath.Dir(e.opts.Path)
	stdin, err := e.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := e.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := e.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := e.cmd.Start(); err != nil {
		return fmt.Errorf("start engine: %w", err)
	}

	e.in = stdin
	e.readDone = make(chan struct{})

	go e.drainStderr(stderr)
	go e.readLoop(stdout)
	return nil
}

func (e *Engine) drainStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	for sc.Scan() {
		e.opts.Logger.Warn("%s <stderr>: %s", e.opts.Name, sc.Text())
	}
}

func (e *Engine) readLoop(r io.Reader) {
	defer close(e.readDone)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		e.opts.Logger.Info("%s < %s", e.opts.Name, line)
		e.handleLine(line)
	}
	if err := sc.Err(); err != nil {
		e.readErr = err
	}
	// If a Go() was in-flight, signal termination.
	e.mu.Lock()
	ch := e.goCh
	e.goCh = nil
	e.mu.Unlock()
	if ch != nil {
		ch <- Event{Error: errors.New("engine stdout closed before bestmove")}
		close(ch)
	}
}

func (e *Engine) handleLine(line string) {
	switch {
	case strings.HasPrefix(line, "id name "):
		e.mu.Lock()
		e.idName = strings.TrimPrefix(line, "id name ")
		e.mu.Unlock()
	case strings.HasPrefix(line, "id author "):
		e.mu.Lock()
		e.idAuthor = strings.TrimPrefix(line, "id author ")
		e.mu.Unlock()
	case strings.HasPrefix(line, "option "):
		opt := parseOption(line)
		if opt.Name != "" {
			e.mu.Lock()
			e.engineOpt[opt.Name] = opt
			e.mu.Unlock()
		}
	case line == "usiok":
		select {
		case e.usiOKCh <- struct{}{}:
		default:
		}
	case line == "readyok":
		select {
		case e.readyCh <- struct{}{}:
		default:
		}
	case strings.HasPrefix(line, "info "):
		info, err := ParseInfo(line)
		if err != nil {
			return
		}
		e.routeEvent(Event{Info: &info})
	case strings.HasPrefix(line, "bestmove "):
		bm, err := ParseBestMove(line)
		if err != nil {
			e.opts.Logger.Warn("parse bestmove: %v", err)
			return
		}
		e.routeEvent(Event{BestMove: &bm})
	}
}

func (e *Engine) routeEvent(ev Event) {
	e.mu.Lock()
	ch := e.goCh
	if ev.BestMove != nil {
		e.goCh = nil
	}
	e.mu.Unlock()
	if ch == nil {
		return
	}
	ch <- ev
	if ev.BestMove != nil {
		close(ch)
	}
}

// send writes a single command line, serialized with other writes.
func (e *Engine) send(cmd string) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	e.opts.Logger.Info("%s > %s", e.opts.Name, cmd)
	_, err := io.WriteString(e.in, cmd+"\n")
	return err
}

// Setoption is one (name, value) pair to be emitted as
// "setoption name <Name> value <Value>" during Handshake. An empty Value
// emits "setoption name <Name>" (button-type options). Order is
// preserved as given — some engines react to setoptions immediately and
// require a specific sequence (e.g. NumGPUs / OnnxModel before Threads).
type Setoption struct {
	Name  string
	Value string
}

// Handshake performs "usi → usiok", sends setoption lines for opts in
// the supplied order, then "isready → readyok". Unknown options (not
// advertised by the engine) are sent anyway — many engines accept them
// silently.
func (e *Engine) Handshake(ctx context.Context, opts []Setoption) error {
	if err := e.send("usi"); err != nil {
		return err
	}
	if err := e.waitSignal(ctx, e.usiOKCh, e.opts.HandshakeTimeout, "usiok"); err != nil {
		return err
	}
	for _, o := range opts {
		var cmd string
		if o.Value == "" {
			cmd = fmt.Sprintf("setoption name %s", o.Name)
		} else {
			cmd = fmt.Sprintf("setoption name %s value %s", o.Name, o.Value)
		}
		if err := e.send(cmd); err != nil {
			return err
		}
	}
	return e.Ready(ctx)
}

// Ready sends "isready" and waits for "readyok".
func (e *Engine) Ready(ctx context.Context) error {
	if err := e.send("isready"); err != nil {
		return err
	}
	return e.waitSignal(ctx, e.readyCh, e.opts.ReadyTimeout, "readyok")
}

func (e *Engine) waitSignal(ctx context.Context, ch <-chan struct{}, timeout time.Duration, what string) error {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-e.readDone:
		return fmt.Errorf("engine exited before %s", what)
	case <-t.C:
		return fmt.Errorf("timeout waiting for %s", what)
	}
}

// NewGame sends "usinewgame".
func (e *Engine) NewGame() error { return e.send("usinewgame") }

// Gameover sends "gameover win|lose|draw".
func (e *Engine) Gameover(result string) error {
	return e.send("gameover " + result)
}

// Stop sends "stop". Engines reply with a final bestmove.
func (e *Engine) Stop() error { return e.send("stop") }

// Quit sends "quit" and waits for the process to exit, or kills it after
// QuitTimeout. Returns any exit error.
func (e *Engine) Quit(ctx context.Context) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	e.mu.Unlock()

	// Best-effort "quit"; ignore write errors (engine may have died).
	_ = e.send("quit")
	_ = e.in.Close()

	t := time.NewTimer(e.opts.QuitTimeout)
	defer t.Stop()
	done := make(chan error, 1)
	go func() { done <- e.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = e.cmd.Process.Kill()
		<-done
		return ctx.Err()
	case <-t.C:
		_ = e.cmd.Process.Kill()
		<-done
		return fmt.Errorf("engine did not exit within %s; killed", e.opts.QuitTimeout)
	}
}

// Go sends "position <positionCmd>" followed by "go <tc>" and returns a
// channel that receives Info updates and finally one BestMove (then is
// closed). Only one Go call may be active at a time.
func (e *Engine) Go(ctx context.Context, positionCmd string, tc TimeControl) (<-chan Event, error) {
	e.mu.Lock()
	if e.goCh != nil {
		e.mu.Unlock()
		return nil, errors.New("engine: a Go is already in-flight")
	}
	ch := make(chan Event, 32)
	e.goCh = ch
	e.mu.Unlock()

	if err := e.send(positionCmd); err != nil {
		e.clearGoCh()
		return nil, err
	}
	if err := e.send("go " + tc.FormatGo()); err != nil {
		e.clearGoCh()
		return nil, err
	}
	return ch, nil
}

func (e *Engine) clearGoCh() {
	e.mu.Lock()
	e.goCh = nil
	e.mu.Unlock()
}

// IDName / IDAuthor / Option accessors for the bridge/TUI to show.
func (e *Engine) IDName() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.idName
}

func (e *Engine) IDAuthor() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.idAuthor
}

// parseOption parses a single "option name X type Y [default ...] [min ...] [max ...] [var ...]*" line.
func parseOption(line string) EngineOption {
	opt := EngineOption{}
	tokens := strings.Fields(strings.TrimPrefix(line, "option "))
	// Walk tokens, accumulating values for the active key (name tokens can
	// contain spaces, but we only keep single-token names for simplicity —
	// YaneuraOu and similar engines don't use multi-word names).
	i := 0
	for i < len(tokens) {
		key := tokens[i]
		i++
		if i >= len(tokens) {
			break
		}
		val := tokens[i]
		i++
		switch key {
		case "name":
			opt.Name = val
		case "type":
			opt.Type = val
		case "default":
			opt.Default = val
		case "min":
			opt.Min = val
		case "max":
			opt.Max = val
		case "var":
			opt.Vars = append(opt.Vars, val)
		}
	}
	return opt
}

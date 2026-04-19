// Command shgterm is a terminal-based USI-CSA bridge: it connects a local
// USI shogi engine to a remote CSA protocol game server and renders the
// running game in the terminal.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jh85/shgterm/internal/bridge"
	"github.com/jh85/shgterm/internal/config"
	"github.com/jh85/shgterm/internal/tui"
)

const usage = `usage: shgterm [flags] <config.yaml|config.json>

Connects a USI engine to a CSA game server and plays games in the terminal.
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "shgterm:", err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("shgterm", flag.ContinueOnError)
	flags.Usage = func() {
		fmt.Fprint(os.Stderr, usage)
		flags.PrintDefaults()
	}
	serverName := flags.String("server", "", "name of the entry to use from config's 'servers' map")
	listServers := flags.Bool("list-servers", false, "print the server names from config and exit")
	host := flags.String("host", "", "override selected server.host")
	port := flags.Int("port", 0, "override selected server.port")
	id := flags.String("id", "", "override selected server.id")
	password := flags.String("password", "", "override selected server.password")
	proto := flags.String("protocol", "", "override selected server.protocolVersion (v121|v121_floodgate)")
	repeat := flags.Int("repeat", 0, "override repeat count")
	logDir := flags.String("log-dir", "logs", "directory for log files (empty disables file logging)")
	recordDir := flags.String("record-dir", "records", "directory for saved game records")
	ascii := flags.Bool("ascii", false, "render pieces as USI ASCII instead of kanji")
	flip := flags.Bool("flip", false, "force-flip board (default: auto-flip if we are White)")
	noTUI := flags.Bool("no-tui", false, "disable TUI (stderr log-only mode)")
	logLevel := flags.String("log-level", "info", "log level: debug|info|warn|error")

	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		flags.Usage()
		return errors.New("expected exactly one config file path")
	}

	cfg, err := config.Load(flags.Arg(0))
	if err != nil {
		return err
	}
	if *listServers {
		names := cfg.ServerNames()
		if len(names) == 0 {
			fmt.Println("(no 'servers' map in config; using legacy single-server form)")
			return nil
		}
		for _, n := range names {
			s := cfg.Servers[n]
			marker := "  "
			if n == cfg.DefaultServer {
				marker = "* "
			}
			fmt.Printf("%s%-16s  %s:%d  [%s]  id=%s\n", marker, n, s.Host, s.Port, s.ProtocolVersion, s.ID)
		}
		if cfg.DefaultServer != "" {
			fmt.Println("(* = defaultServer)")
		}
		return nil
	}
	selected, err := cfg.SelectServer(*serverName)
	if err != nil {
		return err
	}
	if selected != "" {
		fmt.Fprintf(os.Stderr, "shgterm: using server %q (%s:%d, %s)\n",
			selected, cfg.Server.Host, cfg.Server.Port, cfg.Server.ProtocolVersion)
	}
	applyOverrides(cfg, *host, *port, *id, *password, *proto, *repeat)
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config invalid after overrides: %w", err)
	}

	// When the TUI owns the terminal, stderr would corrupt it — force
	// file-only logging unless the user passed an explicit --log-dir="".
	if !*noTUI && *logDir == "" {
		*logDir = "logs"
	}
	logger, closeLog, err := setupLogger(*logDir, *logLevel, *noTUI)
	if err != nil {
		return err
	}
	defer closeLog()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("shgterm starting",
		"engine", cfg.USI.Path,
		"server", fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		"protocol", cfg.Server.ProtocolVersion,
		"repeat", cfg.Repeat,
	)

	opts := bridge.Options{
		Config:    cfg,
		Logger:    slogAdapter{logger},
		RecordDir: *recordDir,
	}

	if *noTUI {
		opts.UI = bridge.NewStderrUI(os.Stderr)
		if err := bridge.Run(ctx, opts); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	}

	// TUI mode: run bridge in goroutine; tui owns main goroutine.
	t, err := tui.New(cancel, *ascii, *flip)
	if err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	opts.UI = t

	bridgeErr := make(chan error, 1)
	go func() {
		err := bridge.Run(ctx, opts)
		bridgeErr <- err
		// Notify the TUI that the bridge loop has finished. We deliberately
		// do NOT cancel the context here — shgterm stays open so the user
		// can review the final state. The user presses 'q' (confirmed) to
		// actually exit.
		t.SessionEnded(err)
	}()

	_ = t.Run(ctx)

	// User quit (or SIGINT). Cancel the bridge if it's still running and
	// wait for cleanup.
	cancel()
	select {
	case err := <-bridgeErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	case <-time.After(10 * time.Second):
		logger.Warn("bridge did not finish within 10s; exiting")
	}
	return nil
}

func applyOverrides(cfg *config.Config, host string, port int, id, password, proto string, repeat int) {
	if host != "" {
		cfg.Server.Host = host
	}
	if port != 0 {
		cfg.Server.Port = port
	}
	if id != "" {
		cfg.Server.ID = id
	}
	if password != "" {
		cfg.Server.Password = password
	}
	if proto != "" {
		cfg.Server.ProtocolVersion = config.ProtocolVersion(proto)
	}
	if repeat != 0 {
		cfg.Repeat = repeat
	}
}

func setupLogger(dir, level string, allowStderr bool) (*slog.Logger, func(), error) {
	lvl := slog.LevelInfo
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, func() {}, fmt.Errorf("mkdir log: %w", err)
		}
		fname := filepath.Join(dir, "shgterm-"+time.Now().Format("20060102-150405")+".log")
		f, err := os.OpenFile(fname, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, func() {}, fmt.Errorf("open log: %w", err)
		}
		h := slog.NewTextHandler(f, &slog.HandlerOptions{Level: lvl})
		return slog.New(h), func() { _ = f.Close() }, nil
	}
	if !allowStderr {
		// Silent: TUI active and no file requested.
		return slog.New(slog.NewTextHandler(nopWriter{}, &slog.HandlerOptions{Level: slog.LevelError})), func() {}, nil
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	return slog.New(h), func() {}, nil
}

// slogAdapter adapts *slog.Logger to the bridge.Logger interface.
type slogAdapter struct{ l *slog.Logger }

func (a slogAdapter) Info(msg string, args ...any) { a.l.Info(fmt.Sprintf(msg, args...)) }
func (a slogAdapter) Warn(msg string, args ...any) { a.l.Warn(fmt.Sprintf(msg, args...)) }
func (a slogAdapter) Err(msg string, args ...any)  { a.l.Error(fmt.Sprintf(msg, args...)) }

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

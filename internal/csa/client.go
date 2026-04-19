package csa

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// State is the current CSA client state.
type State int

const (
	StateIdle State = iota
	StateConnecting
	StateConnected
	StateWaitingGameSummary
	StateGameSummary
	StateGameTime  // both sides
	StateGameTimeB // +
	StateGameTimeW // -
	StateGamePosition
	StateReady
	StateWaitingStart
	StateWaitingReject
	StatePlaying
	StateWaitingLogout
	StateClosed
)

// Client is one CSA session.
type Client struct {
	opts Options
	log  Logger

	connMu sync.Mutex
	conn   net.Conn
	br     *bufio.Reader

	stateMu sync.Mutex
	state   State

	events    chan Event
	readDone  chan struct{}
	closeOnce sync.Once

	// Accumulators for the active Game_Summary block. Only touched from
	// the reader goroutine.
	summary      *GameSummary
	positionBuf  strings.Builder
	nextTimeSide *PlayerColor

	// Blank-line ping timer (driven by time.AfterFunc).
	pingMu   sync.Mutex
	pingTmr  *time.Timer
	pingStop bool
}

// New creates an unconnected client.
func New(opts Options) *Client {
	if opts.Logger == nil {
		opts.Logger = nopLogger{}
	}
	if opts.DialTimeout == 0 {
		opts.DialTimeout = 30 * time.Second
	}
	if opts.Protocol == "" {
		opts.Protocol = V121Floodgate
	}
	return &Client{
		opts:   opts,
		log:    opts.Logger,
		state:  StateIdle,
		events: make(chan Event, 32),
	}
}

// Events returns the read-only event channel. It is closed when the client
// terminates.
func (c *Client) Events() <-chan Event { return c.events }

// SetBlankLinePing configures the blank-line ping timings. Must be called
// before Dial/Attach; values <=0 disable the feature.
func (c *Client) SetBlankLinePing(initialDelay, interval time.Duration) {
	c.opts.BlankLinePing.InitialDelay = initialDelay
	c.opts.BlankLinePing.Interval = interval
}

// State returns the current client state (concurrent-safe).
func (c *Client) State() State {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.state
}

func (c *Client) setState(s State) {
	c.stateMu.Lock()
	c.state = s
	c.stateMu.Unlock()
}

func (c *Client) emit(ev Event) { c.events <- ev }

// Dial opens the TCP connection (with keepalive) and sends LOGIN.
func (c *Client) Dial(ctx context.Context) error {
	addr := net.JoinHostPort(c.opts.Host, strconv.Itoa(c.opts.Port))
	c.setState(StateConnecting)

	dialer := net.Dialer{Timeout: c.opts.DialTimeout, KeepAlive: c.opts.Keepalive}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("csa: dial %s: %w", addr, err)
	}
	return c.Attach(conn)
}

// Attach binds an already-connected net.Conn as the CSA transport, sends
// LOGIN, and starts the reader goroutine.
func (c *Client) Attach(conn net.Conn) error {
	c.connMu.Lock()
	if c.conn != nil {
		c.connMu.Unlock()
		return errors.New("csa: already attached")
	}
	c.conn = conn
	c.br = bufio.NewReader(conn)
	c.connMu.Unlock()

	c.setState(StateConnected)
	c.emit(Event{Kind: EventConnected})

	if err := c.rawSend(fmt.Sprintf("LOGIN %s %s", c.opts.ID, c.opts.Password)); err != nil {
		return fmt.Errorf("csa: send LOGIN: %w", err)
	}

	c.readDone = make(chan struct{})
	go c.readLoop()
	return nil
}

// Close closes the connection and drains state. Safe to call multiple times.
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.stopPing()
		c.connMu.Lock()
		if c.conn != nil {
			err = c.conn.Close()
		}
		c.connMu.Unlock()
		if c.readDone != nil {
			<-c.readDone
		}
		c.setState(StateClosed)
		close(c.events)
	})
	return err
}

// ---- send path -----------------------------------------------------------

func (c *Client) rawSend(line string) error {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	if c.conn == nil {
		return errors.New("csa: no connection")
	}
	c.log.Info("csa > %s", redactLogin(line, c.opts.Password))
	if _, err := io.WriteString(c.conn, line+"\n"); err != nil {
		return err
	}
	c.armPing(line == "")
	return nil
}

func redactLogin(line, password string) string {
	if password == "" {
		return line
	}
	return strings.ReplaceAll(line, password, "*****")
}

// Agree sends AGREE for the given game id.
func (c *Client) Agree(gameID string) error {
	if c.State() != StateReady {
		return fmt.Errorf("csa: AGREE rejected in state %v", c.State())
	}
	if c.summary == nil || c.summary.ID != gameID {
		return fmt.Errorf("csa: AGREE id mismatch")
	}
	c.setState(StateWaitingStart)
	return c.rawSend("AGREE " + gameID)
}

// Reject sends REJECT for the given game id.
func (c *Client) Reject(gameID string) error {
	if c.State() != StateReady {
		return fmt.Errorf("csa: REJECT rejected in state %v", c.State())
	}
	c.setState(StateWaitingReject)
	return c.rawSend("REJECT " + gameID)
}

// SendMove sends our move. If Protocol is V121Floodgate and comment != "",
// it appends ",'<comment>" (comment should begin with "* ", e.g. "* 42 7g7f 3c3d").
func (c *Client) SendMove(move, comment string) error {
	if c.State() != StatePlaying {
		return fmt.Errorf("csa: move rejected in state %v", c.State())
	}
	line := move
	if c.opts.Protocol == V121Floodgate && comment != "" {
		line += ",'" + comment
	}
	return c.rawSend(line)
}

// Resign sends %TORYO.
func (c *Client) Resign() error {
	if c.State() != StatePlaying {
		return fmt.Errorf("csa: %%TORYO rejected in state %v", c.State())
	}
	return c.rawSend("%TORYO")
}

// DeclareWin sends %KACHI (Jishogi declaration).
func (c *Client) DeclareWin() error {
	if c.State() != StatePlaying {
		return fmt.Errorf("csa: %%KACHI rejected in state %v", c.State())
	}
	return c.rawSend("%KACHI")
}

// Chudan sends %CHUDAN.
func (c *Client) Chudan() error {
	if c.State() != StatePlaying {
		return fmt.Errorf("csa: %%CHUDAN rejected in state %v", c.State())
	}
	return c.rawSend("%CHUDAN")
}

// Logout ends the session. Floodgate simply half-closes the write side;
// v121 sends LOGOUT and waits for LOGOUT:completed on the reader side.
func (c *Client) Logout() error {
	if c.opts.Protocol == V121Floodgate {
		c.setState(StateWaitingLogout)
		return c.halfCloseWrite()
	}
	switch c.State() {
	case StateWaitingGameSummary, StateGameSummary, StateGameTime,
		StateGameTimeB, StateGameTimeW, StateGamePosition, StateReady:
		c.setState(StateWaitingLogout)
		return c.rawSend("LOGOUT")
	default:
		return fmt.Errorf("csa: LOGOUT invalid in state %v", c.State())
	}
}

func (c *Client) halfCloseWrite() error {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	if tc, ok := c.conn.(*net.TCPConn); ok {
		return tc.CloseWrite()
	}
	return c.conn.Close()
}

// ---- receive path --------------------------------------------------------

func (c *Client) readLoop() {
	defer close(c.readDone)
	for {
		line, err := c.br.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			c.log.Info("csa < %s", line)
			c.armPing(false)
			if line != "" {
				c.handleLine(line)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				c.emit(Event{Kind: EventClosed})
			} else if !errors.Is(err, net.ErrClosed) {
				c.emit(Event{Kind: EventError, Err: err})
			}
			return
		}
	}
}

var loginOKRe = regexp.MustCompile(`^LOGIN:.+ OK$`)

func (c *Client) handleLine(line string) {
	// Multi-line blocks (Game_Summary / Time / Position) and in-play
	// streams are dispatched on state; single-line control commands match
	// by pattern.
	switch c.State() {
	case StateGameSummary:
		c.onGameSummaryLine(line)
		return
	case StateGameTime, StateGameTimeB, StateGameTimeW:
		c.onGameTimeLine(line)
		return
	case StateGamePosition:
		c.onGamePositionLine(line)
		return
	case StatePlaying:
		c.onPlayingLine(line)
		return
	}

	switch {
	case loginOKRe.MatchString(line):
		c.setState(StateWaitingGameSummary)
		c.emit(Event{Kind: EventLoginOK})
	case line == "LOGIN:incorrect":
		c.emit(Event{Kind: EventLoginFailed, Err: errors.New("LOGIN:incorrect")})
	case line == "LOGOUT:completed":
		c.setState(StateWaitingLogout)
	case line == "BEGIN Game_Summary":
		c.setState(StateGameSummary)
		c.summary = &GameSummary{}
		c.positionBuf.Reset()
	case strings.HasPrefix(line, "REJECT:"):
		c.setState(StateWaitingGameSummary)
		c.emit(Event{Kind: EventRejectedByServer})
	case strings.HasPrefix(line, "START:"):
		c.setState(StatePlaying)
		c.emit(Event{Kind: EventStart, Summary: c.summary})
	default:
		c.log.Warn("csa: unknown line in state %v: %q", c.State(), line)
	}
}

func (c *Client) onGameSummaryLine(line string) {
	switch line {
	case "END Game_Summary":
		if s := c.summary; s != nil {
			s.Position = c.positionBuf.String()
		}
		c.setState(StateReady)
		c.emit(Event{Kind: EventGameSummary, Summary: c.summary})
		return
	case "BEGIN Time":
		c.setState(StateGameTime)
		c.nextTimeSide = nil
		return
	case "BEGIN Time+":
		c.setState(StateGameTimeB)
		b := Black
		c.nextTimeSide = &b
		return
	case "BEGIN Time-":
		c.setState(StateGameTimeW)
		w := White
		c.nextTimeSide = &w
		return
	case "BEGIN Position":
		c.setState(StateGamePosition)
		return
	}
	k, v, ok := splitKV(line)
	if !ok {
		c.log.Warn("csa: unexpected Game_Summary line %q", line)
		return
	}
	s := c.summary
	switch k {
	case "Protocol_Version":
		s.ProtocolVersion = v
	case "Protocol_Mode":
		s.ProtocolMode = v
	case "Format":
		s.Format = v
	case "Declaration":
		s.Declaration = v
	case "Rematch_On_Draw":
		s.RematchOnDraw = v
	case "Max_Moves":
		s.MaxMoves, _ = strconv.Atoi(v)
	case "Game_ID":
		s.ID = v
	case "Name+":
		s.Players[Black].Name = v
	case "Name-":
		s.Players[White].Name = v
	case "Your_Turn":
		if v == "+" {
			s.MyColor = Black
		} else {
			s.MyColor = White
		}
	case "To_Move":
		if v == "+" {
			s.ToMove = Black
		} else {
			s.ToMove = White
		}
	default:
		// Tolerate unknown keys (spec extensions).
	}
}

func (c *Client) onGameTimeLine(line string) {
	switch line {
	case "END Time", "END Time+", "END Time-":
		c.setState(StateGameSummary)
		c.nextTimeSide = nil
		return
	}
	k, v, ok := splitKV(line)
	if !ok {
		c.log.Warn("csa: unexpected Time line %q", line)
		return
	}
	sides := []PlayerColor{Black, White}
	if c.nextTimeSide != nil {
		sides = []PlayerColor{*c.nextTimeSide}
	}
	for _, side := range sides {
		tc := &c.summary.Players[side].Time
		switch k {
		case "Time_Unit":
			tc.TimeUnit = parseTimeUnit(v)
		case "Total_Time":
			tc.TotalTimeUnits, _ = strconv.ParseInt(v, 10, 64)
		case "Byoyomi":
			tc.ByoyomiUnits, _ = strconv.ParseInt(v, 10, 64)
		case "Increment":
			tc.IncrementUnits, _ = strconv.ParseInt(v, 10, 64)
		case "Delay":
			tc.DelayUnits, _ = strconv.ParseInt(v, 10, 64)
		case "Least_Time_Per_Move":
			tc.LeastPerMove, _ = strconv.ParseInt(v, 10, 64)
		case "Time_Roundup":
			tc.TimeRoundup = strings.EqualFold(v, "YES")
		}
	}
}

func (c *Client) onGamePositionLine(line string) {
	if line == "END Position" {
		c.setState(StateGameSummary)
		return
	}
	c.positionBuf.WriteString(line)
	c.positionBuf.WriteByte('\n')
}

var moveTimeRe = regexp.MustCompile(`,T([0-9]+)$`)

func (c *Client) onPlayingLine(line string) {
	switch {
	case strings.HasPrefix(line, "+"), strings.HasPrefix(line, "-"):
		color := Black
		if line[0] == '-' {
			color = White
		}
		var elapsed int64
		if m := moveTimeRe.FindStringSubmatch(line); m != nil {
			elapsed, _ = strconv.ParseInt(m[1], 10, 64)
		}
		c.emit(Event{Kind: EventMove, Move: line, Color: color, ElapsedUnits: elapsed})
	case strings.HasPrefix(line, "#"):
		switch line {
		case "#RESIGN", "#SENNICHITE", "#OUTE_SENNICHITE",
			"#ILLEGAL_MOVE", "#ILLEGAL_ACTION", "#TIME_UP",
			"#JISHOGI", "#MAX_MOVES":
			c.emit(Event{Kind: EventSpecialMove, Special: line})
		case "#WIN", "#LOSE", "#DRAW", "#CENSORED", "#CHUDAN":
			c.setState(StateWaitingGameSummary)
			c.emit(Event{Kind: EventResult, Result: line})
		default:
			c.log.Warn("csa: unknown '#' line in play: %q", line)
		}
	case strings.HasPrefix(line, "%"):
		// Server echo of declaration commands — ignore.
	default:
		c.log.Warn("csa: unknown line in play: %q", line)
	}
}

func splitKV(line string) (string, string, bool) {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return "", "", false
	}
	return line[:i], line[i+1:], true
}

func parseTimeUnit(s string) time.Duration {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasSuffix(s, "msec"):
		n, err := strconv.ParseInt(s[:len(s)-4], 10, 64)
		if err == nil {
			return time.Duration(n) * time.Millisecond
		}
	case strings.HasSuffix(s, "sec"):
		n, err := strconv.ParseInt(s[:len(s)-3], 10, 64)
		if err == nil {
			return time.Duration(n) * time.Second
		}
	case strings.HasSuffix(s, "min"):
		n, err := strconv.ParseInt(s[:len(s)-3], 10, 64)
		if err == nil {
			return time.Duration(n) * time.Minute
		}
	}
	return time.Second
}

// ---- blank-line ping -----------------------------------------------------

// armPing schedules the next blank-line ping. fromBlank indicates that the
// activity that triggered this arm was itself an outbound blank line (use
// Interval); otherwise use InitialDelay.
func (c *Client) armPing(fromBlank bool) {
	if c.opts.BlankLinePing.InitialDelay <= 0 {
		return
	}
	delay := c.opts.BlankLinePing.InitialDelay
	if fromBlank && c.opts.BlankLinePing.Interval > 0 {
		delay = c.opts.BlankLinePing.Interval
	}
	c.pingMu.Lock()
	defer c.pingMu.Unlock()
	if c.pingStop {
		return
	}
	if c.pingTmr != nil {
		c.pingTmr.Stop()
	}
	c.pingTmr = time.AfterFunc(delay, c.fireBlankPing)
}

func (c *Client) fireBlankPing() {
	// Send a blank line; rawSend will re-arm via armPing(fromBlank=true).
	_ = c.rawSend("")
}

func (c *Client) stopPing() {
	c.pingMu.Lock()
	defer c.pingMu.Unlock()
	c.pingStop = true
	if c.pingTmr != nil {
		c.pingTmr.Stop()
		c.pingTmr = nil
	}
}

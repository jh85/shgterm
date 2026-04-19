// Package csa implements a CSA shogi game server client covering the
// protocol versions v121 and v121_floodgate. It is I/O only; higher-level
// game logic (move application, kifu recording) lives in the bridge.
package csa

import "time"

// Logger mirrors the minimal logger used elsewhere in shgterm.
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Err(msg string, args ...any)
}

type nopLogger struct{}

func (nopLogger) Info(string, ...any) {}
func (nopLogger) Warn(string, ...any) {}
func (nopLogger) Err(string, ...any)  {}

// Protocol selects the CSA dialect. v121 follows the base spec; floodgate
// allows appending an eval/PV comment to our moves and does not use LOGOUT.
type Protocol string

const (
	V121          Protocol = "v121"
	V121Floodgate Protocol = "v121_floodgate"
)

// PlayerColor identifies a CSA side.
type PlayerColor uint8

const (
	Black PlayerColor = iota // +
	White                    // -
)

func (c PlayerColor) CSASign() byte {
	if c == Black {
		return '+'
	}
	return '-'
}

// Opponent returns the other color.
func (c PlayerColor) Opponent() PlayerColor { return c ^ 1 }

// TimeConfig collects the time control keys sent in BEGIN Time / Time+ /
// Time- blocks. All durations are in "time units" except TimeUnit itself.
type TimeConfig struct {
	TimeUnit       time.Duration // duration of one unit (default 1s if absent)
	TotalTimeUnits int64
	ByoyomiUnits   int64
	IncrementUnits int64
	DelayUnits     int64
	LeastPerMove   int64 // Least_Time_Per_Move
	TimeRoundup    bool
}

// PlayerInfo is the name + time control for one side.
type PlayerInfo struct {
	Name string
	Time TimeConfig
}

// GameSummary carries everything from a single BEGIN…END Game_Summary block.
// Callers use it to agree/reject, configure engine time, and feed the
// starting position to the shogi package.
type GameSummary struct {
	ID              string
	ProtocolVersion string
	ProtocolMode    string
	Format          string
	Declaration     string
	RematchOnDraw   string
	MaxMoves        int
	MyColor         PlayerColor
	ToMove          PlayerColor
	Players         [2]PlayerInfo
	// Position is the raw CSA position block (BEGIN Position..END Position
	// contents, newline-separated). Pass to shogi.ParseCSAPosition.
	Position string
}

// Options configure the CSA client.
type Options struct {
	Host       string
	Port       int
	ID         string
	Password   string
	Protocol   Protocol
	Logger     Logger
	DialTimeout time.Duration

	// Keepalive is the TCP-layer SO_KEEPALIVE initial delay. 0 means off.
	Keepalive time.Duration

	// BlankLinePing: when InitialDelay>0, a blank line is sent after that
	// long of inactivity; subsequent blanks are sent every Interval.
	BlankLinePing struct {
		InitialDelay time.Duration
		Interval     time.Duration
	}
}

// EventKind enumerates the kinds of events the client emits.
type EventKind int

const (
	EventConnected EventKind = iota
	EventLoginOK
	EventLoginFailed
	EventGameSummary
	EventStart
	EventRejectedByServer
	EventMove
	EventSpecialMove
	EventResult
	EventClosed
	EventError
)

// Event is one asynchronous update from the client's read loop.
type Event struct {
	Kind        EventKind
	Summary     *GameSummary // EventGameSummary
	Move        string       // EventMove raw, e.g. "+7776FU,T10"
	Color       PlayerColor  // EventMove
	ElapsedUnits int64       // EventMove, parsed from ",T<n>"
	Special     string       // EventSpecialMove: "#RESIGN", "#TIME_UP", etc.
	Result      string       // EventResult: "#WIN"/"#LOSE"/"#DRAW"/"#CENSORED"/"#CHUDAN"
	Err         error        // EventError
}

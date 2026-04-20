package bridge

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/jh85/shgterm/internal/csa"
	"github.com/jh85/shgterm/internal/shogi"
	"github.com/jh85/shgterm/internal/usi"
)

// TestClockDecrementsOnMoveEcho is a regression test for the TIME_UP bug:
// the local clock[] must be mutated in place by awaitMoveOrEnd so that
// subsequent engine 'go' commands see a shrinking btime/wtime budget.
func TestClockDecrementsOnMoveEcho(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()

	// Start the "server" goroutine FIRST so it's ready to consume the
	// LOGIN line synchronously; net.Pipe has no buffering and Attach's
	// LOGIN write would otherwise block the test goroutine forever.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 1024)
		_, _ = serverConn.Read(buf) // LOGIN line
		lines := []string{
			"LOGIN:x OK",
			"BEGIN Game_Summary",
			"Protocol_Version:1.2",
			"Format:Shogi 1.0",
			"Game_ID:test",
			"Name+:black",
			"Name-:white",
			"Your_Turn:-",
			"To_Move:+",
			"BEGIN Time",
			"Time_Unit:1sec",
			"Total_Time:300",
			"Byoyomi:0",
			"Increment:10",
			"END Time",
			"BEGIN Position",
			"PI",
			"+",
			"END Position",
			"END Game_Summary",
			"START:test",
			"+7776FU,T24",
		}
		for _, l := range lines {
			_, _ = serverConn.Write([]byte(l + "\n"))
		}
	}()

	client := csa.New(csa.Options{Host: "pipe", ID: "x", Password: "y", Protocol: csa.V121Floodgate})
	if err := client.Attach(clientConn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// Drain the pre-play events.
	events := client.Events()
	for {
		ev := <-events
		if ev.Kind == csa.EventStart {
			break
		}
	}

	// Now simulate the per-turn state that playOneGame maintains.
	clock := [2]int64{310, 310} // initial = total 300 + increment 10
	usiMoves := []string{}
	pos := shogi.NewStartPosition()
	summary := &csa.GameSummary{
		ID:      "test",
		MyColor: csa.White,
		Players: [2]csa.PlayerInfo{
			{Time: csa.TimeConfig{TimeUnit: time.Second, TotalTimeUnits: 300, IncrementUnits: 10}},
			{Time: csa.TimeConfig{TimeUnit: time.Second, TotalTimeUnits: 300, IncrementUnits: 10}},
		},
	}
	res := &gameResult{summary: summary}

	done, err := awaitMoveOrEnd(context.Background(), client, pos, &usiMoves, res, &clock, time.Second, nullUI{})
	if err != nil {
		t.Fatalf("awaitMoveOrEnd: %v", err)
	}
	if done {
		t.Fatal("unexpected done=true for a normal move echo")
	}

	// After Black takes 24s and gets +10 increment back: 310 - 24 + 10 = 296.
	if clock[csa.Black] != 296 {
		t.Errorf("clock[Black] = %d, want 296 (mutation must propagate through pointer)", clock[csa.Black])
	}
	// White's clock unchanged.
	if clock[csa.White] != 310 {
		t.Errorf("clock[White] = %d, want 310", clock[csa.White])
	}

	wg.Wait()
}

// nullUI is a stub implementing bridge.UI for the test.
type nullUI struct{}

func (nullUI) SetEngine(string, string)                    {}
func (nullUI) SetGame(*csa.GameSummary, csa.PlayerColor)   {}
func (nullUI) SetPosition(*shogi.Position, string, string) {}
func (nullUI) SetClock(int64, int64, time.Duration)        {}
func (nullUI) SetTurnTimer(csa.PlayerColor, time.Time)     {}
func (nullUI) SetPV(usi.Info)                              {}
func (nullUI) LogLine(string, string)                      {}
func (nullUI) GameEnded(string, string)                    {}
func (nullUI) SessionEnded(error)                          {}

package csa

import (
	"bufio"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer drives one side of a net.Pipe with scripted send/recv actions.
type fakeServer struct {
	conn net.Conn
	br   *bufio.Reader
}

func newFakeServer(t *testing.T) (*fakeServer, net.Conn) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	return &fakeServer{conn: serverConn, br: bufio.NewReader(serverConn)}, clientConn
}

func (s *fakeServer) send(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(s.conn, line+"\n"); err != nil {
		t.Fatalf("server write %q: %v", line, err)
	}
}

func (s *fakeServer) readLine(t *testing.T) string {
	t.Helper()
	line, err := s.br.ReadString('\n')
	if err != nil && err != io.EOF {
		t.Fatalf("server read: %v", err)
	}
	return strings.TrimRight(line, "\r\n")
}

func (s *fakeServer) close() { _ = s.conn.Close() }

// runServer runs f in a goroutine and returns a wait func.
func runServer(f func()) func() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		f()
	}()
	return wg.Wait
}

func TestLoginAndGameSummary(t *testing.T) {
	srv, clientConn := newFakeServer(t)
	defer srv.close()

	c := New(Options{
		Host: "pipe", Port: 0, ID: "alice", Password: "secret",
		Protocol: V121Floodgate,
	})

	var gotOurMove string
	wait := runServer(func() {
		// 1) Consume client's LOGIN.
		if got := srv.readLine(t); got != "LOGIN alice secret" {
			t.Errorf("login: got %q", got)
			return
		}
		// 2) Send LOGIN OK + a full Game_Summary.
		srv.send(t, "LOGIN:alice OK")
		srv.send(t, "BEGIN Game_Summary")
		srv.send(t, "Protocol_Version:1.2")
		srv.send(t, "Format:Shogi 1.0")
		srv.send(t, "Game_ID:test-123")
		srv.send(t, "Name+:alice")
		srv.send(t, "Name-:bob")
		srv.send(t, "Your_Turn:+")
		srv.send(t, "To_Move:+")
		srv.send(t, "BEGIN Time")
		srv.send(t, "Time_Unit:1sec")
		srv.send(t, "Total_Time:900")
		srv.send(t, "Byoyomi:10")
		srv.send(t, "Increment:0")
		srv.send(t, "END Time")
		srv.send(t, "BEGIN Position")
		srv.send(t, "PI")
		srv.send(t, "+")
		srv.send(t, "END Position")
		srv.send(t, "END Game_Summary")

		// 3) Consume AGREE, start game, send opponent move.
		if got := srv.readLine(t); got != "AGREE test-123" {
			t.Errorf("agree: got %q", got)
			return
		}
		srv.send(t, "START:test-123")
		srv.send(t, "+7776FU,T10")

		// 4) Consume our move (Floodgate comment).
		gotOurMove = srv.readLine(t)

		// 5) End the game.
		srv.send(t, "#RESIGN")
		srv.send(t, "#WIN")
	})

	if err := c.Attach(clientConn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	events := c.Events()
	mustEvent(t, events, EventConnected)
	mustEvent(t, events, EventLoginOK)

	summaryEv := mustEvent(t, events, EventGameSummary)
	s := summaryEv.Summary
	if s == nil || s.ID != "test-123" {
		t.Fatalf("summary id: %+v", s)
	}
	if s.Players[Black].Name != "alice" || s.Players[White].Name != "bob" {
		t.Fatalf("player names: %+v", s.Players)
	}
	if s.MyColor != Black || s.ToMove != Black {
		t.Fatalf("colors: my=%v toMove=%v", s.MyColor, s.ToMove)
	}
	if s.Players[Black].Time.TotalTimeUnits != 900 || s.Players[Black].Time.ByoyomiUnits != 10 {
		t.Fatalf("time: %+v", s.Players[Black].Time)
	}
	if s.Players[Black].Time.TimeUnit != time.Second {
		t.Fatalf("time unit: %v", s.Players[Black].Time.TimeUnit)
	}
	if !strings.Contains(s.Position, "PI") {
		t.Fatalf("position block missing PI: %q", s.Position)
	}

	if err := c.Agree("test-123"); err != nil {
		t.Fatalf("agree: %v", err)
	}
	mustEvent(t, events, EventStart)

	mv := mustEvent(t, events, EventMove)
	if mv.Color != Black || mv.ElapsedUnits != 10 || !strings.Contains(mv.Move, "+7776FU") {
		t.Fatalf("move event: %+v", mv)
	}

	if err := c.SendMove("-3334FU", "* -45 3c3d 2g2f"); err != nil {
		t.Fatalf("send move: %v", err)
	}

	mustEvent(t, events, EventSpecialMove)
	res := mustEvent(t, events, EventResult)
	if res.Result != "#WIN" {
		t.Fatalf("result: %+v", res)
	}

	wait()
	if gotOurMove != "-3334FU,'* -45 3c3d 2g2f" {
		t.Fatalf("our move on wire: %q", gotOurMove)
	}
}

func TestLoginIncorrect(t *testing.T) {
	srv, clientConn := newFakeServer(t)
	defer srv.close()

	c := New(Options{Host: "pipe", ID: "x", Password: "y"})

	wait := runServer(func() {
		_ = srv.readLine(t) // consume LOGIN
		srv.send(t, "LOGIN:incorrect")
	})

	if err := c.Attach(clientConn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	mustEvent(t, c.Events(), EventConnected)
	e := mustEvent(t, c.Events(), EventLoginFailed)
	if e.Err == nil {
		t.Fatal("expected Err on login failure")
	}
	wait()
}

func mustEvent(t *testing.T, ch <-chan Event, want EventKind) Event {
	t.Helper()
	select {
	case ev := <-ch:
		if ev.Kind != want {
			t.Fatalf("got event kind %v (%+v), want %v", ev.Kind, ev, want)
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout waiting for %v", want)
	}
	return Event{}
}

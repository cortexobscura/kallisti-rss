package bot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func floodConnection(t *testing.T, events []Event) *Connection {
	t.Helper()
	f := &Fixture{Posts: map[string]Job{}, Events: events}
	srv := httptest.NewServer(http.HandlerFunc(f.Handler))
	t.Cleanup(srv.Close)
	c, e := dialConnection(context.Background(), Config{Server: srv.URL, Origin: srv.URL, Username: "fixture", Channels: []int64{1}, Admins: []uint32{7}}, "fixture-only-token", srv.Client())
	must(t, e)
	t.Cleanup(c.Close)
	return c
}

// An unprivileged user must not be able to disconnect the bot by posting
// more "!feed" lines than the command queue holds while the loop is busy.
func TestUnauthorizedCommandFloodKeepsConnection(t *testing.T) {
	stranger, admin := uint32(99), uint32(7)
	var events []Event
	for i := 1; i <= 600; i++ {
		events = append(events, Event{Type: "message", Message: Message{ID: int64(i), Channel: 1, AuthorID: &stranger, Verified: true, Text: "!feed list"}})
	}
	events = append(events, Event{Type: "message", Message: Message{ID: 10000, Channel: 1, AuthorID: &admin, Verified: true, Text: "!feed help"}})
	c := floodConnection(t, events)
	select {
	case m := <-c.Commands:
		if m.ID != 10000 {
			t.Fatalf("unauthorized command queued: %d", m.ID)
		}
	case <-c.Done():
		t.Fatal("unauthorized command flood closed the connection")
	case <-time.After(2 * time.Second):
		t.Fatal("authorized command not received")
	}
	select {
	case <-c.Done():
		t.Fatal("connection closed after flood")
	case <-time.After(50 * time.Millisecond):
	}
}

// A burst of authorized commands beyond the queue drops the excess with a
// warning instead of tearing down the connection (which also loses them).
func TestAuthorizedCommandOverflowDropsInsteadOfDisconnecting(t *testing.T) {
	admin := uint32(7)
	var events []Event
	for i := 1; i <= 300; i++ {
		events = append(events, Event{Type: "message", Message: Message{ID: int64(i), Channel: 1, AuthorID: &admin, Verified: true, Text: fmt.Sprintf("!feed pause %d", i)}})
	}
	c := floodConnection(t, events)
	deadline := time.After(2 * time.Second)
	for len(c.Commands) < cap(c.Commands) {
		select {
		case <-c.Done():
			t.Fatal("authorized overflow closed the connection")
		case <-deadline:
			t.Fatalf("queue never filled: %d", len(c.Commands))
		case <-time.After(5 * time.Millisecond):
		}
	}
	select {
	case <-c.Done():
		t.Fatal("authorized overflow closed the connection")
	case <-time.After(100 * time.Millisecond):
	}
	if first := <-c.Commands; first.ID != 1 {
		t.Fatalf("queue order changed: first=%d", first.ID)
	}
}

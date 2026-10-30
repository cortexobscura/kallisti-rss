package bot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func rotatingEntries(round, n int) []Entry {
	v := make([]Entry, n)
	for i := range v {
		v[i] = Entry{ID: fmt.Sprintf("r%d-%d", round, i), Title: "spam"}
	}
	return v
}

func unresolved(t *testing.T, s *Store, channel int64) int {
	t.Helper()
	var n int
	must(t, s.DB.QueryRow("SELECT count(*) FROM outbox WHERE channel=? AND state IN ('pending','blocked')", channel).Scan(&n))
	return n
}

// One feed that changes every entry ID on each poll must not exhaust the
// global outbox and stall ingestion for subscriptions in other channels.
func TestRotatingFeedCannotStarveOtherChannels(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	must(t, s.Seed([]Seed{{Channel: 1, URL: "https://bad.example/feed"}, {Channel: 2, URL: "https://good.example/feed"}}))
	subs, e := s.Subscriptions()
	must(t, e)
	bad, good := subs[0], subs[1]
	must(t, s.Ingest(ctx, bad, nil))
	must(t, s.Ingest(ctx, good, nil))
	bad.Initialized, good.Initialized = true, true
	for round := 0; round < 12; round++ {
		must(t, s.Ingest(ctx, bad, rotatingEntries(round, 1000)))
	}
	if n := unresolved(t, s, 1); n > ChannelPendingLimit {
		t.Fatalf("one channel holds %d unresolved jobs, limit %d", n, ChannelPendingLimit)
	}
	must(t, s.Ingest(ctx, good, []Entry{{ID: "fresh", Title: "fresh"}}))
	if n := unresolved(t, s, 2); n != 1 {
		t.Fatalf("other channel queued %d jobs, want 1", n)
	}
}

// Excess new entries are deferred, not discarded: they stay unseen and are
// queued oldest-first on later polls while still present in the feed.
func TestNewEntryCapDefersOldestFirst(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	must(t, s.Seed([]Seed{{Channel: 1, URL: "https://busy.example/feed"}}))
	subs, e := s.Subscriptions()
	must(t, e)
	sub := subs[0]
	must(t, s.Ingest(ctx, sub, nil))
	sub.Initialized = true
	total := MaxNewPerPoll + 10
	entries := make([]Entry, total)
	for i := range entries { // newest first, as feeds usually publish
		entries[i] = Entry{ID: fmt.Sprintf("e%03d", total-1-i), Title: fmt.Sprintf("e%03d", total-1-i)}
	}
	r, e := s.IngestCounts(ctx, sub, entries)
	must(t, e)
	if r.Queued != MaxNewPerPoll || r.Deferred != 10 {
		t.Fatalf("first poll queued=%d deferred=%d", r.Queued, r.Deferred)
	}
	r, e = s.IngestCounts(ctx, sub, entries)
	must(t, e)
	if r.Queued != 10 || r.Deferred != 0 {
		t.Fatalf("second poll queued=%d deferred=%d", r.Queued, r.Deferred)
	}
	rows, e := s.DB.Query("SELECT payload FROM outbox ORDER BY id")
	must(t, e)
	defer rows.Close()
	i := 0
	for rows.Next() {
		var p string
		must(t, rows.Scan(&p))
		if want := fmt.Sprintf("\"text\":\"e%03d", i); !strings.Contains(p, want) {
			t.Fatalf("job %d out of order: %s", i, p)
		}
		i++
	}
	if i != total {
		t.Fatalf("queued %d jobs, want %d", i, total)
	}
}

func TestPollLogsDeferredEntries(t *testing.T) {
	logs := captureLogs(t)
	s := testStore(t)
	url := "https://busy.example/private/path"
	cfg := Config{Poll: 5 * time.Minute, Feeds: []Seed{{Channel: 1, URL: url}}}
	must(t, s.Seed(cfg.Feeds))
	f := fixtureFetch{url: []byte(`<rss><item><guid>old</guid><title>old</title></item></rss>`)}
	eng := Engine{Config: cfg, Store: s, Fetcher: f}
	must(t, eng.Poll(context.Background()))
	var b strings.Builder
	b.WriteString("<rss>")
	for i := 0; i < MaxNewPerPoll+5; i++ {
		fmt.Fprintf(&b, "<item><guid>n%d</guid><title>n%d</title></item>", i, i)
	}
	b.WriteString("</rss>")
	f[url] = []byte(b.String())
	must(t, eng.Poll(context.Background()))
	if n := unresolved(t, s, 1); n != MaxNewPerPoll {
		t.Fatalf("queued %d, want %d", n, MaxNewPerPoll)
	}
	out := logs.String()
	if !strings.Contains(out, `"msg":"feed entries deferred"`) || !strings.Contains(out, `"deferred":5`) || !strings.Contains(out, `"queued":25`) {
		t.Fatalf("deferral not logged: %s", out)
	}
	if strings.Contains(out, "/private/path") {
		t.Fatal("feed URL path leaked into logs")
	}
}

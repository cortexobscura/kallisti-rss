package bot

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRegisteredIdentityRestartGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := OpenStore(path)
	must(t, err)
	jobs := queueJobs(t, s, 1)
	must(t, s.BindUser(0))
	must(t, s.BindUser(0))
	must(t, s.Close())
	s, err = OpenStore(path)
	must(t, err)
	defer s.Close()
	if s.BindUser(42) == nil {
		t.Fatal("changed identity accepted")
	}
	must(t, s.BindUser(0))
	after, err := s.Inspect()
	must(t, err)
	if !reflect.DeepEqual(jobs, after) {
		t.Fatal("identity rejection changed queue")
	}
}

func TestChatManagementLifecycleAndRollback(t *testing.T) {
	s := testStore(t)
	admin := uint32(42)
	ctx := context.Background()
	e := Engine{Config: Config{Channels: []int64{1, 2}, Admins: []uint32{admin}}, Store: s}
	command := func(id int64, text string) error {
		return e.Command(ctx, Message{ID: id, Channel: 1, AuthorID: &admin, Verified: true, Text: text})
	}
	must(t, command(1, "!feed add https://example.com/feed"))
	subs, err := s.Subscriptions()
	must(t, err)
	if len(subs) != 1 || subs[0].Initialized {
		t.Fatal("add unexpectedly baselined")
	}
	must(t, s.Ingest(ctx, subs[0], []Entry{{ID: "seen", Title: "Old"}}))
	must(t, command(2, "!feed pause 1"))
	subs, err = s.Subscriptions()
	must(t, err)
	if !subs[0].Paused {
		t.Fatal("pause had no effect")
	}
	must(t, command(3, "!feed resume 1"))
	subs, err = s.Subscriptions()
	must(t, err)
	if subs[0].Paused {
		t.Fatal("resume had no effect")
	}
	must(t, s.Seed([]Seed{{Channel: 2, URL: "https://other.example/feed"}}))
	must(t, command(4, "!feed list"))
	listed, err := s.Inspect()
	must(t, err)
	reply := listed[len(listed)-1].Payload.Text
	if !strings.Contains(reply, "https://example.com/feed") || strings.Contains(reply, "other.example") {
		t.Fatal("list incorrect/channel leak")
	}
	must(t, command(5, "!feed remove 1"))
	must(t, command(6, "!feed add https://example.com/feed"))
	subs, err = s.Subscriptions()
	must(t, err)
	if len(subs) != 2 || subs[0].Paused || !subs[0].Initialized {
		t.Fatal("restore lost history/state")
	}
	before, err := s.Inspect()
	must(t, err)
	must(t, s.Ingest(ctx, subs[0], []Entry{{ID: "seen", Title: "Old"}}))
	after, err := s.Inspect()
	must(t, err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("restored seen item duplicated")
	}
	must(t, command(7, "!feed add https://127.0.0.1/feed"))
	subs, err = s.Subscriptions()
	must(t, err)
	if len(subs) != 2 {
		t.Fatal("private URL added")
	}
	// A failed confirmation enqueue must roll back mutation AND replay marker.
	_, err = s.DB.Exec(`CREATE TRIGGER fail_command_reply BEFORE INSERT ON outbox BEGIN SELECT RAISE(FAIL,'fixture failure'); END`)
	must(t, err)
	if command(8, "!feed pause 1") == nil {
		t.Fatal("expected transaction failure")
	}
	subs, err = s.Subscriptions()
	must(t, err)
	if subs[0].Paused {
		t.Fatal("mutation survived failed reply")
	}
	var n int
	must(t, s.DB.QueryRow("SELECT count(*) FROM commands WHERE id=8").Scan(&n))
	if n != 0 {
		t.Fatal("replay marker committed on failure")
	}
	_, err = s.DB.Exec("DROP TRIGGER fail_command_reply")
	must(t, err)
	must(t, command(8, "!feed pause 1"))
	must(t, command(8, "!feed pause 1"))
	subs, err = s.Subscriptions()
	must(t, err)
	if !subs[0].Paused {
		t.Fatal("retry failed")
	}
}

func TestSharedCacheBoundKeepsSubscriptionOrder(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	f := &countedFetch{fixtureFetch: fixtureFetch{}}
	var seeds []Seed
	for i := 0; i < 10; i++ {
		u := "https://example.com/" + string(rune('a'+i))
		f.fixtureFetch[u] = []byte(`<rss><item><guid>old</guid><title>Old</title></item></rss>`)
		seeds = append(seeds, Seed{Channel: 1, URL: u})
	}
	for _, seed := range append([]Seed(nil), seeds...) {
		seeds = append(seeds, Seed{Channel: 2, URL: seed.URL})
	}
	must(t, s.Seed(seeds))
	now := time.Unix(2000000000, 0)
	e := Engine{Store: s, Fetcher: f, Now: func() time.Time { return now }}
	must(t, e.PollDue(ctx))
	if f.calls != 12 {
		t.Fatal("expected eight cached shared results and two refetches", f.calls)
	}
	for u := range f.fixtureFetch {
		f.fixtureFetch[u] = []byte(`<rss><item><guid>fresh</guid><title>` + u[len(u)-1:] + `</title></item></rss>`)
	}
	now = now.Add(5 * time.Minute)
	must(t, e.PollDue(ctx))
	jobs, err := s.Inspect()
	must(t, err)
	if len(jobs) != 20 {
		t.Fatal("missing independent ingests")
	}
	for i, j := range jobs {
		want := int64(1 + i/10)
		if j.Channel != want || j.Payload.Text != string(rune('a'+i%10)) {
			t.Fatal("traversal order changed", i, j.Channel, j.Payload.Text)
		}
	}
}

package bot

import (
	"context"
	"fmt"
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

// Repeated pause/resume must say the subscription was already in that state
// instead of reporting a change that did not happen.
func TestPauseResumeReportsCurrentState(t *testing.T) {
	s := testStore(t)
	admin := uint32(42)
	ctx := context.Background()
	e := Engine{Config: Config{Channels: []int64{1, 2}, Admins: []uint32{admin}}, Store: s}
	must(t, s.Seed([]Seed{{Channel: 1, URL: "https://example.com/feed"}}))
	none := "No matching subscription in this channel."
	for i, c := range []struct {
		channel    int64
		text, want string
	}{
		{1, "!feed resume 1", "Subscription 1 is already active; nothing changed."},
		{1, "!feed pause 1", "Subscription 1 paused. Already queued posts remain in the outbox."},
		{1, "!feed pause 1", "Subscription 1 is already paused; nothing changed."},
		{2, "!feed resume 1", none}, // another channel cannot see or change it
		{1, "!feed resume 1", "Subscription 1 resumed."},
		{1, "!feed resume 1", "Subscription 1 is already active; nothing changed."},
		{1, "!feed pause 9", none},
		{1, "!feed remove 1", "Subscription 1 removed. Already queued posts remain in the outbox."},
		{1, "!feed resume 1", none},
	} {
		must(t, e.Command(ctx, Message{ID: int64(i + 1), Channel: c.channel, AuthorID: &admin, Verified: true, Text: c.text}))
		jobs, err := s.Inspect()
		must(t, err)
		if got := jobs[len(jobs)-1].Payload.Text; got != c.want {
			t.Fatalf("step %d %q: reply %q, want %q", i, c.text, got, c.want)
		}
	}
}

// Add must say whether it created, restored or found the subscription, and
// must not silently resume a paused one.
func TestAddReportsWhatHappened(t *testing.T) {
	s := testStore(t)
	admin := uint32(42)
	ctx := context.Background()
	e := Engine{Config: Config{Channels: []int64{1, 2}, Admins: []uint32{admin}}, Store: s}
	url := "https://example.com/feed"
	for i, c := range []struct {
		channel    int64
		text, want string
	}{
		{1, "!feed add " + url, "Subscription 1 added. Its first poll records current entries without posting them."},
		{1, "!feed add " + url, "Subscription 1 already exists; nothing changed."},
		{1, "!feed pause 1", "Subscription 1 paused. Already queued posts remain in the outbox."},
		{1, "!feed add " + url, "Subscription 1 already exists and is paused; nothing changed. Use !feed resume 1 to resume it."},
		{1, "!feed remove 1", "Subscription 1 removed. Already queued posts remain in the outbox."},
		{1, "!feed add 1", "!feed add takes a feed URL, not an ID. To restore a removed subscription, add its URL again."},
		{1, "!feed add https://127.0.0.1/feed", "Feed URL rejected."},
		{1, "!feed add " + url, "Subscription 1 restored with its seen history. Entries it has not seen that are still in the feed may post."},
		{2, "!feed add " + url, "Subscription 2 added. Its first poll records current entries without posting them."},
	} {
		must(t, e.Command(ctx, Message{ID: int64(i + 1), Channel: c.channel, AuthorID: &admin, Verified: true, Text: c.text}))
		jobs, err := s.Inspect()
		must(t, err)
		if got := jobs[len(jobs)-1].Payload.Text; got != c.want {
			t.Fatalf("step %d %q: reply %q, want %q", i, c.text, got, c.want)
		}
	}
	subs, err := s.Subscriptions()
	must(t, err)
	if len(subs) != 2 || subs[0].Paused || subs[1].Paused {
		t.Fatalf("unexpected subscriptions: %+v", subs)
	}
}

// Re-adding an active subscription at the limit is not a limit error.
func TestAddExistingAtLimitReportsExisting(t *testing.T) {
	s := testStore(t)
	admin := uint32(42)
	e := Engine{Config: Config{Channels: []int64{1}, Admins: []uint32{admin}}, Store: s}
	var seeds []Seed
	for i := 0; i < 100; i++ {
		seeds = append(seeds, Seed{Channel: 1, URL: fmt.Sprintf("https://example.com/%d", i)})
	}
	must(t, s.Seed(seeds))
	for i, c := range []struct{ text, want string }{
		{"!feed add https://example.com/0", "Subscription 1 already exists; nothing changed."},
		{"!feed add https://example.com/new", "Subscription limit reached."},
	} {
		must(t, e.Command(context.Background(), Message{ID: int64(i + 1), Channel: 1, AuthorID: &admin, Verified: true, Text: c.text}))
		jobs, err := s.Inspect()
		must(t, err)
		if got := jobs[len(jobs)-1].Payload.Text; got != c.want {
			t.Fatalf("%q: reply %q, want %q", c.text, got, c.want)
		}
	}
}

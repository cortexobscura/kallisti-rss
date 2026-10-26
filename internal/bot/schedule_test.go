package bot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type countedFetch struct {
	fixtureFetch
	calls int
}

func (f *countedFetch) Fetch(ctx context.Context, u string, n int64) ([]byte, error) {
	f.calls++
	return f.fixtureFetch.Fetch(ctx, u, n)
}
func TestChannelSchedule(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	url := "https://example.com/feed"
	must(t, s.Seed([]Seed{{Channel: 1, URL: url}, {Channel: 2, URL: url}, {Channel: 3, URL: url}}))
	f := &countedFetch{fixtureFetch: fixtureFetch{url: []byte(`<rss><item><guid>old</guid><title>Old</title></item></rss>`)}}
	now := time.Unix(2000000000, 0)
	cfg := Config{Poll: 2 * time.Minute, ChannelPollIntervals: map[int64]time.Duration{1: time.Minute, 2: 3 * time.Minute}}
	e := Engine{Config: cfg, Store: s, Fetcher: f, Now: func() time.Time { return now }}
	must(t, e.PollDue(ctx))
	if f.calls != 1 {
		t.Fatal("shared baseline fetch", f.calls)
	}
	jobs, err := s.Inspect()
	must(t, err)
	if len(jobs) != 0 {
		t.Fatal("baseline flood")
	}
	f.fixtureFetch[url] = []byte(`<rss><item><guid>new</guid><title>New</title></item><item><guid>old</guid><title>Old</title></item></rss>`)
	now = now.Add(time.Minute)
	must(t, e.PollDue(ctx))
	jobs, err = s.Inspect()
	must(t, err)
	if len(jobs) != 1 || jobs[0].Channel != 1 {
		t.Fatal("slow channel delivered early", jobs)
	}
	must(t, e.PollDue(ctx))
	if f.calls != 2 {
		t.Fatal("hot poll")
	}
	now = now.Add(time.Minute)
	must(t, e.PollDue(ctx))
	if f.calls != 3 {
		t.Fatal("same due URL not shared")
	}
	jobs, err = s.Inspect()
	must(t, err)
	if len(jobs) != 2 || jobs[1].Channel != 3 {
		t.Fatal("global fallback", jobs)
	}
	now = now.Add(time.Minute)
	must(t, e.PollDue(ctx))
	jobs, err = s.Inspect()
	must(t, err)
	if len(jobs) != 3 || jobs[2].Channel != 2 {
		t.Fatal("slow channel missed item", jobs)
	}
	// A malformed shared result affects due channels only and never advances seen state.
	f.fixtureFetch[url] = []byte(`<html>private-token</html>`)
	now = now.Add(time.Minute)
	if e.PollDue(ctx) == nil {
		t.Fatal("missing failure")
	}
	calls := f.calls
	must(t, e.PollDue(ctx))
	if f.calls != calls {
		t.Fatal("failure hot retry")
	}
	must(t, s.PauseSubscription(1, 1))
	must(t, s.Seed([]Seed{{Channel: 1, URL: url}}))
	f.fixtureFetch[url] = []byte(`<rss><item><guid>new</guid><title>New</title></item></rss>`)
	// A process restart checks immediately, preserving baseline and pause state.
	e = Engine{Config: cfg, Store: s, Fetcher: f, Now: func() time.Time { return now }}
	must(t, e.PollDue(ctx))
	jobs, err = s.Inspect()
	must(t, err)
	if len(jobs) != 3 {
		t.Fatal("restart duplicate", jobs)
	}
	subs, err := s.Subscriptions()
	must(t, err)
	if !subs[0].Paused {
		t.Fatal("pause lost")
	}
}
func TestChannelIntervalConfig(t *testing.T) {
	base := "server: https://example.com\norigin: https://example.com\nusername: fixture\ntoken_file: absent\nchannels: [1, 2]\n"
	for _, tc := range []struct {
		extra string
		ok    bool
	}{
		{"", true}, {"channel_poll_intervals: {1: 30s, 2: 24h}\n", true},
		{"channel_poll_intervals: {3: 1m}\n", false}, {"channel_poll_intervals: {1: 0s}\n", false},
		{"channel_poll_intervals: {1: -1m}\n", false}, {"channel_poll_intervals: {1: 29s}\n", false},
		{"channel_poll_intervals: {1: 25h}\n", false}, {"channel_poll_intervals: {1: nonsense}\n", false},
		{"channel_poll_intervals: {1: 1m, 1: 2m}\n", false},
	} {
		t.Run(fmt.Sprint(tc.extra), func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.yaml")
			must(t, os.WriteFile(p, []byte(base+tc.extra), 0600))
			c, err := LoadConfig(p)
			if (err == nil) != tc.ok {
				t.Fatal(err)
			}
			if err == nil && tc.extra == "" && c.ChannelPollInterval(1) != 5*time.Minute {
				t.Fatal("default changed")
			}
		})
	}
}

func TestScheduledRestartPreservesPauseAndBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := OpenStore(path)
	must(t, err)
	url := "https://example.com/feed"
	seeds := []Seed{{Channel: 1, URL: url}, {Channel: 2, URL: url}}
	must(t, s.Seed(seeds))
	f := &countedFetch{fixtureFetch: fixtureFetch{url: []byte(`<rss><item><guid>old</guid><title>Old</title></item></rss>`)}}
	e := Engine{Store: s, Fetcher: f}
	must(t, e.PollDue(context.Background()))
	must(t, s.PauseSubscription(1, 1))
	must(t, s.Close())
	s, err = OpenStore(path)
	must(t, err)
	defer s.Close()
	must(t, s.Seed(seeds))
	f.fixtureFetch[url] = []byte(`<rss><item><guid>new</guid><title>New</title></item></rss>`)
	e = Engine{Store: s, Fetcher: f}
	must(t, e.PollDue(context.Background()))
	jobs, err := s.Inspect()
	must(t, err)
	if len(jobs) != 1 || jobs[0].Channel != 2 {
		t.Fatal("restart changed baseline/pause", jobs)
	}
}

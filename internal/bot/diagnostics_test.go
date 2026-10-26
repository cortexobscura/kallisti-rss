package bot

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&b, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &b
}
func TestFetchDiagnosticsRedacted(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500} {
		f := NewFetcher(false)
		f.Client.Transport = roundFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("secret-body")), Header: make(http.Header)}, nil
		})
		_, e := f.Fetch(context.Background(), "https://example.com/secret-path?token=secret-query", 100)
		var d *Diagnostic
		if !errors.As(e, &d) || d.Class != "http_status" || d.Status != status {
			t.Fatalf("wrong diagnostic: %v", e)
		}
		if strings.Contains(e.Error(), "secret") {
			t.Fatal("response or URL leaked")
		}
	}
	cases := []struct {
		err   error
		class string
	}{{context.DeadlineExceeded, "timeout"}, {context.Canceled, "canceled"}, {&net.DNSError{Err: "secret", Name: "secret-host"}, "dns_error"}, {x509.HostnameError{Host: "secret-host"}, "tls_error"}, {errors.New("secret-query"), "network_error"}, {diag("address_blocked", "DNS resolved a nonpublic address"), "address_blocked"}}
	for _, c := range cases {
		err := transportDiagnostic(&url.Error{Op: "Get", URL: "https://user:secret@host/secret?token=secret", Err: c.err})
		var d *Diagnostic
		if !errors.As(err, &d) || d.Class != c.class || strings.Contains(err.Error(), "secret") {
			t.Fatalf("class/redaction: %+v", err)
		}
	}
}
func TestPollLogsSourceAndContinuesWithoutStateAdvance(t *testing.T) {
	logs := captureLogs(t)
	s := testStore(t)
	cfg := Config{Poll: 5 * time.Minute, Feeds: []Seed{{Channel: 1, URL: "https://bad.example/private-token?token=secret"}, {Channel: 1, URL: "https://good.example/feed"}}}
	must(t, s.Seed(cfg.Feeds))
	f := NewFetcher(false)
	f.Client.Transport = roundFunc(func(r *http.Request) (*http.Response, error) {
		status, body := 200, `<rss><channel><item><guid>old</guid><title>old</title></item></channel></rss>`
		if r.URL.Host == "bad.example" {
			status, body = 403, "private response secret"
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	eng := Engine{Config: cfg, Store: s, Fetcher: f}
	if eng.Poll(context.Background()) == nil {
		t.Fatal("failure not returned")
	}
	v, e := s.Subscriptions()
	must(t, e)
	if v[0].Initialized || !v[1].Initialized {
		t.Fatal("failed baseline advanced or good feed not processed")
	}
	var n int
	must(t, s.DB.QueryRow("SELECT count(*) FROM seen WHERE sub=1").Scan(&n))
	if n != 0 {
		t.Fatal("failed seen state advanced")
	}
	j, e := s.Next()
	must(t, e)
	if j != nil {
		t.Fatal("successful baseline flooded")
	}
	text := logs.String()
	for _, want := range []string{`"subscription":1`, `"yaml_feed":1`, `"feed_host":"bad.example"`, `"http_status":403`, `"error_class":"http_status"`, `"baseline_pending":true`, `"succeeded":1`, `"failed":1`, `"baseline_recorded":true`, `"retry":"next_channel_poll"`} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s in %s", want, text)
		}
	}
	for _, secret := range []string{"private-token", "secret", "private response"} {
		if strings.Contains(text, secret) {
			t.Fatal("log leak", secret)
		}
	}
}
func TestParserFailureAndReadOnlyCheck(t *testing.T) {
	logs := captureLogs(t)
	cfg := Config{Feeds: []Seed{{Channel: 1, URL: "https://example.com/secret?token=secret"}}}
	f := fixtureFetch{cfg.Feeds[0].URL: []byte(`<html><body>login secret</body></html>`)}
	if CheckFeeds(context.Background(), cfg, f) == nil {
		t.Fatal("HTML accepted")
	}
	if !strings.Contains(logs.String(), `"error_class":"parse_error"`) || !strings.Contains(logs.String(), "not RSS or Atom") {
		t.Fatal(logs.String())
	}
	if strings.Contains(logs.String(), "secret") {
		t.Fatal("parser leaked body or URL")
	}
	err := parserDiagnostic(errors.New("future parser error secret"))
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("unknown parser text reflected")
	}
}
func TestSubscriptionMappingStable(t *testing.T) {
	s := testStore(t)
	cfg := Config{Feeds: []Seed{{Channel: 1, URL: "https://old.reddit.com/r/LiminalSpace/new.rss"}, {Channel: 1, URL: "https://aniyuki.com/category/avatars/feed/"}, {Channel: 1, URL: "https://www.youtube.com/feeds/videos.xml?channel_id=first"}, {Channel: 1, URL: "https://www.youtube.com/feeds/videos.xml?channel_id=second"}}}
	must(t, s.Seed(cfg.Feeds))
	subs, e := s.Subscriptions()
	must(t, e)
	views := SubscriptionViews(cfg, subs)
	for i, v := range views {
		if v.ID != int64(i+1) || v.YAMLFeed != i+1 {
			t.Fatal("fresh IDs not YAML insertion order", views)
		}
	}
	cfg.Feeds[0], cfg.Feeds[1] = cfg.Feeds[1], cfg.Feeds[0]
	must(t, s.Seed(cfg.Feeds))
	subs, e = s.Subscriptions()
	must(t, e)
	views = SubscriptionViews(cfg, subs)
	if views[0].Host != "old.reddit.com" || views[0].YAMLFeed != 2 || views[1].Host != "aniyuki.com" || views[1].YAMLFeed != 1 {
		t.Fatal("reordering YAML reassigned durable IDs", views)
	}
	if views[2].Key == views[3].Key {
		t.Fatal("same-host query feeds not distinguishable")
	}
}

func TestFailedRefreshPreservesExistingSeenState(t *testing.T) {
	captureLogs(t)
	s := testStore(t)
	seed := Seed{Channel: 1, URL: "https://example.com/feed"}
	must(t, s.Seed([]Seed{seed}))
	subs, e := s.Subscriptions()
	must(t, e)
	must(t, s.Ingest(context.Background(), subs[0], []Entry{{ID: "existing", Title: "old"}}))
	eng := Engine{Config: Config{Feeds: []Seed{seed}}, Store: s, Fetcher: fixtureFetch{seed.URL: []byte("<html>access denied</html>")}}
	if eng.Poll(context.Background()) == nil {
		t.Fatal("failed refresh accepted")
	}
	subs, e = s.Subscriptions()
	must(t, e)
	if !subs[0].Initialized {
		t.Fatal("existing baseline lost")
	}
	var count int
	must(t, s.DB.QueryRow("SELECT count(*) FROM seen WHERE sub=?", subs[0].ID).Scan(&count))
	if count != 1 {
		t.Fatal("existing seen IDs changed")
	}
	j, e := s.Next()
	must(t, e)
	if j != nil {
		t.Fatal("failed refresh queued data")
	}
}

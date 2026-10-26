package bot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestImageStagesTLSAfterBaseline(t *testing.T) {
	for _, mode := range []string{"images", "unsupported", "oversized", "upload_denied", "permission", "ack_lost", "none"} {
		t.Run(mode, func(t *testing.T) {
			logs := captureLogs(t)
			ctx := context.Background()
			s := testStore(t)
			fixture := &Fixture{Posts: map[string]Job{}, LoseAck: mode == "ack_lost"}
			if mode == "permission" {
				fixture.RejectChannels = map[int64]string{1: "you may not use this channel's chat"}
			}
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "upload_denied" && r.URL.Path == "/api/upload" {
					http.Error(w, "private-secret-body", 403)
					return
				}
				fixture.Handler(w, r)
			}))
			defer srv.Close()
			conn, err := dialConnection(ctx, Config{Server: srv.URL, Origin: srv.URL, Username: "fixture"}, "fixture-only-token", srv.Client())
			must(t, err)
			defer conn.Close()
			url := "https://example.com/feed?private-secret-query"
			f := fixtureFetch{url: []byte(`<rss><channel><item><guid>old</guid><title>Old</title></item></channel></rss>`), "https://example.com/a.png?private-secret-query": fixturePNG(), "https://example.com/b.png": fixturePNG()}
			must(t, s.Seed([]Seed{{Channel: 1, URL: url}}))
			e := Engine{Store: s, Fetcher: f, Sender: conn}
			must(t, e.Poll(ctx))
			must(t, e.Drain(ctx))
			fixture.mu.Lock()
			n := len(fixture.Posts)
			fixture.mu.Unlock()
			if n != 0 {
				t.Fatal("baseline posted")
			}
			media := `<enclosure url="https://example.com/a.png?private-secret-query" type="image/png"/><content:encoded><![CDATA[<img src="https://example.com/b.png">]]></content:encoded>`
			if mode == "none" {
				media = ""
			}
			if mode == "unsupported" {
				f["https://example.com/b.png"] = []byte(`<svg>private-secret-body</svg>`)
			}
			if mode == "oversized" {
				f["https://example.com/b.png"] = make([]byte, MediaLimit+1)
			}
			f[url] = []byte(`<rss xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><item><guid>fresh</guid><title>New fixture</title>` + media + `</item></channel></rss>`)
			must(t, e.Poll(ctx))
			err = e.Drain(ctx)
			if mode == "permission" || mode == "ack_lost" {
				if err == nil {
					t.Fatal("expected ambiguous/rejected delivery")
				}
				if strings.Contains(logs.String(), "attachments_acknowledged") {
					t.Fatal("claimed acknowledgment")
				}
				jobs, x := s.Inspect()
				must(t, x)
				if len(jobs) != 1 || len(jobs[0].Payload.Uploads) != 2 {
					t.Fatal("uncertain payload changed")
				}
			} else {
				must(t, err)
				want := 2
				if mode == "unsupported" || mode == "oversized" {
					want = 1
				}
				if mode == "none" || mode == "upload_denied" {
					want = 0
				}
				fixture.mu.Lock()
				for _, j := range fixture.Posts {
					if len(j.Payload.Uploads) != want {
						t.Error("attachment count", len(j.Payload.Uploads), want)
					}
				}
				fixture.mu.Unlock()
				if !strings.Contains(logs.String(), "attachments_acknowledged") {
					t.Fatal("missing attachment acknowledgment count")
				}
			}
			if strings.Contains(logs.String(), "private-secret") {
				t.Fatal("log leaked input", logs.String())
			}
			if mode == "upload_denied" && !strings.Contains(logs.String(), `"text_fallback":true`) {
				t.Fatal("fallback not visible")
			}
			if mode == "unsupported" && !strings.Contains(logs.String(), `"stage":"image_decode"`) {
				t.Fatal("validation stage absent")
			}
			if mode == "oversized" && !strings.Contains(logs.String(), `"stage":"image_fetch"`) {
				t.Fatal("download stage absent")
			}
		})
	}
}

func TestPollStorageFailureCooldown(t *testing.T) {
	s := testStore(t)
	must(t, s.DB.Close())
	now := time.Unix(2000000000, 0)
	e := Engine{Store: s, Now: func() time.Time { return now }}
	if e.PollDue(context.Background()) == nil {
		t.Fatal("missing error")
	}
	if e.PollDue(context.Background()) != nil {
		t.Fatal("hot retry")
	}
	now = now.Add(30 * time.Second)
	if e.PollDue(context.Background()) == nil {
		t.Fatal("no retry")
	}
}

func TestImageRedirectPolicyDiagnostics(t *testing.T) {
	logs := captureLogs(t)
	s := testStore(t)
	tx, err := s.DB.Begin()
	must(t, err)
	must(t, enqueue(tx, 1, Payload{Text: "fixture", Images: []string{"https://example.com/image?private-secret"}}))
	must(t, tx.Commit())
	f := NewFetcher(false)
	calls := 0
	f.Client.Transport = roundFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://127.0.0.1/private-secret"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	sender := &recordSender{}
	e := Engine{Store: s, Fetcher: f, Sender: sender}
	must(t, e.Drain(context.Background()))
	if calls != 1 || len(sender.sent) != 1 || len(sender.sent[0].Payload.Uploads) != 0 {
		t.Fatal("redirect policy failed")
	}
	if strings.Contains(logs.String(), "private-secret") || !strings.Contains(logs.String(), `"stage":"image_fetch"`) || !strings.Contains(logs.String(), `"text_fallback":true`) {
		t.Fatal("unsafe/missing diagnostics", logs.String())
	}
}

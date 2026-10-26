package bot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"io"

	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, e := OpenStore(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func TestFeedVariants(t *testing.T) {
	cases := []struct{ name, xml, title, link, image string }{
		{"rss", `<rss><channel><item><guid>x</guid><title>Hello &amp; world</title><link>https://example.com/post</link><description>&lt;p&gt;Summary &lt;script&gt;evil&lt;/script&gt; @everyone&lt;/p&gt;</description><enclosure url="https://example.com/a.jpg" type="image/jpeg"/></item></channel></rss>`, "Hello & world", "https://example.com/post", "https://example.com/a.jpg"},
		{"wordpress", `<rss xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><item><guid>https://example.com/?p=42</guid><title>Gallery</title><link>/post</link><content:encoded><![CDATA[<p>Art</p><img src="/one.png"><img src="/two.jpg"><img src="/one.png"><img src="/three.gif"><img src="/four.webp">]]></content:encoded></item></channel></rss>`, "Gallery", "https://example.com/post", "https://example.com/one.png"},
		{"reddit", `<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>t3_synthetic</id><title>Empty halls</title><link href="https://example.com/post"/><content type="html">&lt;table&gt;&lt;tr&gt;&lt;td&gt;&lt;img src="https://example.com/thumb.jpg"/&gt;&lt;/td&gt;&lt;/tr&gt;&lt;/table&gt;</content></entry></feed>`, "Empty halls", "https://example.com/post", "https://example.com/thumb.jpg"},
		{"youtube", `<feed xmlns:media="http://search.yahoo.com/mrss/"><entry><id>video:synthetic</id><title>News</title><link href="https://example.com/watch?v=x"/><media:group><media:thumbnail url="https://example.com/thumb.jpg"/></media:group></entry></feed>`, "News", "https://example.com/watch?v=x", "https://example.com/thumb.jpg"},
		{"atom", `<feed><entry><id>a</id><title>Atom</title><link href="/post"/><link rel="enclosure" type="image/png" href="/art.png"/></entry></feed>`, "Atom", "https://example.com/post", "https://example.com/art.png"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, e := Parse([]byte(c.xml), "https://example.com/feed")
			must(t, e)
			if len(v) != 1 || v[0].Title != c.title || v[0].Link != c.link || v[0].Image != c.image {
				t.Fatalf("unexpected entry: %+v", v)
			}
			if strings.Contains(v[0].Summary, "evil") || strings.Contains(v[0].Summary, "@everyone") {
				t.Fatal("unsafe summary")
			}
			if c.name == "wordpress" && len(v[0].Images) != 4 {
				t.Fatalf("image dedup: %+v", v[0].Images)
			}
		})
	}
}
func TestRejectFeeds(t *testing.T) {
	for _, b := range []string{`<html>login</html>`, `<!DOCTYPE rss [<!ENTITY x SYSTEM "file:///etc/passwd">]><rss/>`, `<rss><channel>`, strings.Repeat("<x>", 65) + strings.Repeat("</x>", 65), strings.Repeat("x", FeedLimit+1)} {
		if _, e := Parse([]byte(b), "https://example.com"); e == nil {
			t.Fatal("accepted invalid feed")
		}
	}
}
func TestIdentityAndText(t *testing.T) {
	a, _ := Parse([]byte(`<rss><item><guid>a</guid><title>Old</title></item></rss>`), "https://example.com")
	b, _ := Parse([]byte(`<rss><item><guid>a</guid><title>Edited</title></item></rss>`), "https://example.com")
	if a[0].ID != b[0].ID {
		t.Fatal("GUID identity changed")
	}
	if len([]rune(Text(Entry{Title: strings.Repeat("猫", 2200)}))) != 2000 {
		t.Fatal("rune bound")
	}
}
func TestURLPolicy(t *testing.T) {
	for _, s := range []string{"http://example.com", "https://user:pass@example.com", "file:///tmp/a", "https://127.0.0.1", "https://[::ffff:127.0.0.1]", "https://example.com:8443", "https://localhost", "https://host.local", "https://example.com/#fragment"} {
		if _, e := ValidateURL(s, false); e == nil {
			t.Fatalf("accepted %s", s)
		}
	}
	if _, e := ValidateURL("https://example.com/feed?x=1", false); e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"127.0.0.1", "10.1.1.1", "169.254.169.254", "100.64.1.1", "192.0.2.1", "198.18.0.1", "::1", "fc00::1", "fe80::1", "::ffff:10.1.1.1", "64:ff9b::a00:1", "2001:db8::1"} {
		if PublicIP(netip.MustParseAddr(s)) {
			t.Fatalf("accepted %s", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !PublicIP(netip.MustParseAddr(s)) {
			t.Fatalf("rejected public %s", s)
		}
	}
}

type roundFunc func(*http.Request) (*http.Response, error)

func (f roundFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestFetchBoundsAndRedaction(t *testing.T) {
	f := NewFetcher(false)
	f.Client.Transport = roundFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Fatal("credential leak")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("12345")), Header: make(http.Header)}, nil
	})
	if _, e := f.Fetch(context.Background(), "https://example.com/?token=secret", 4); e == nil {
		t.Fatal("unbounded")
	}
	f.Client.Transport = roundFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("secret") })
	_, e := f.Fetch(context.Background(), "https://example.com/?token=secret", 100)
	if e == nil || strings.Contains(e.Error(), "secret") {
		t.Fatal("error leaked URL")
	}
	r, _ := http.NewRequest("GET", "https://127.0.0.1", nil)
	if NewFetcher(false).Client.CheckRedirect(r, []*http.Request{{}}) == nil {
		t.Fatal("private redirect allowed")
	}
	tr := NewFetcher(false).Client.Transport.(*http.Transport)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if c, e := tr.DialContext(ctx, "tcp", "127.0.0.1:80"); e == nil {
		c.Close()
		t.Fatal("private dial allowed")
	}
}
func TestRaster(t *testing.T) {
	im := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var jpg, g bytes.Buffer
	must(t, jpeg.Encode(&jpg, im, nil))
	must(t, gif.Encode(&g, im, nil))
	for _, b := range [][]byte{fixturePNG(), jpg.Bytes(), g.Bytes()} {
		v, e := Raster(b)
		must(t, e)
		if !bytes.HasPrefix(v, []byte("\x89PNG")) {
			t.Fatal("not normalized")
		}
	}
	for _, b := range [][]byte{[]byte(`<svg/>`), []byte("broken"), make([]byte, MediaLimit+1)} {
		if _, e := Raster(b); e == nil {
			t.Fatal("accepted unsafe image")
		}
	}
	b := fixturePNG()
	binary.BigEndian.PutUint32(b[16:20], 100000)
	if _, e := Raster(b); e == nil {
		t.Fatal("accepted oversized dimensions")
	}
}
func TestStoreLifecycle(t *testing.T) {
	s := testStore(t)
	must(t, s.Bind("server|bot"))
	if s.Bind("other|bot") == nil {
		t.Fatal("identity changed")
	}
	must(t, s.Seed([]Seed{{Channel: 1, URL: "https://example.com", MaxImages: 2}, {Channel: 2, URL: "https://example.com"}}))
	subs, e := s.Subscriptions()
	must(t, e)
	ctx := context.Background()
	old := Entry{ID: "old", Title: "old"}
	fresh := Entry{ID: "new", Title: "new", Images: []string{"a", "b", "c"}}
	for _, sub := range subs {
		must(t, s.Ingest(ctx, sub, []Entry{old}))
		must(t, s.Ingest(ctx, sub, []Entry{fresh, old}))
	}
	j, e := s.Next()
	must(t, e)
	if j == nil || j.Channel != 1 || len(j.Payload.Images) != 2 {
		t.Fatalf("outbox %+v", j)
	}
	saved := j.Nonce
	must(t, s.Prepare(j, time.Unix(100000, 0)))
	same, e := s.Next()
	must(t, e)
	if same.Nonce != saved || same.First != 100000 {
		t.Fatal("retry identity changed")
	}
	if s.Prepare(same, time.Unix(100000+23*3600, 0)) == nil {
		t.Fatal("stale not blocked")
	}
	j, e = s.Next()
	must(t, e)
	if j.Channel != 2 || j.Nonce == saved {
		t.Fatal("channel dedup collision")
	}
	must(t, s.Delivered(j.ID))
	status, e := s.Status()
	must(t, e)
	if status != "pending=0 blocked=1 delivered=1" {
		t.Fatal(status)
	}
}
func TestFailedBaselineAndCommands(t *testing.T) {
	s := testStore(t)
	must(t, s.Seed([]Seed{{Channel: 1, URL: "https://example.com"}}))
	eng := Engine{Config: Config{Channels: []int64{1, 2}, Admins: []uint32{0}}, Store: s, Fetcher: fixtureFetch{"https://example.com": []byte("<html>login</html>")}}
	if eng.Poll(context.Background()) == nil {
		t.Fatal("invalid feed accepted")
	}
	subs, _ := s.Subscriptions()
	if subs[0].Initialized {
		t.Fatal("failed baseline marked complete")
	}
	zero := uint32(0)
	wrong := uint32(5)
	for _, m := range []Message{{ID: 1, Channel: 1, AuthorID: &zero, Text: "!feed remove 1"}, {ID: 2, Channel: 1, Verified: true, Text: "!feed remove 1"}, {ID: 3, Channel: 1, AuthorID: &wrong, Verified: true, Text: "!feed remove 1"}, {ID: 4, Channel: 3, AuthorID: &zero, Verified: true, Text: "!feed remove 1"}, {ID: 5, Channel: 2, AuthorID: &zero, Verified: true, Text: "!feed remove 1"}} {
		must(t, eng.Command(context.Background(), m))
	}
	subs, _ = s.Subscriptions()
	if len(subs) != 1 {
		t.Fatal("unauthorized/channel mutation")
	}
	m := Message{ID: 6, Channel: 1, AuthorID: &zero, Verified: true, Text: "!feed remove 1"}
	must(t, eng.Command(context.Background(), m))
	must(t, eng.Command(context.Background(), m))
	must(t, s.Seed([]Seed{{Channel: 1, URL: "https://example.com"}}))
	subs, _ = s.Subscriptions()
	if len(subs) != 0 {
		t.Fatal("removed seed resurrected")
	}
	var n int
	must(t, s.DB.QueryRow("SELECT count(*) FROM commands WHERE id=6").Scan(&n))
	if n != 1 {
		t.Fatal("command replay")
	}
}
func TestExclusiveLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "db")
	a, e := OpenStore(p)
	must(t, e)
	defer a.Close()
	b, e := OpenStore(p)
	if e == nil {
		b.Close()
		t.Fatal("second replica allowed")
	}
}
func TestWireRetryAndRestart(t *testing.T) {
	f := &Fixture{Posts: map[string]Job{}, LoseAck: true}
	srv := httptest.NewServer(http.HandlerFunc(f.Handler))
	defer srv.Close()
	ctx := context.Background()
	cfg := Config{Server: srv.URL, Origin: srv.URL, Username: "fixture", Channels: []int64{1}}
	c, e := dialConnection(ctx, cfg, "fixture-only-token", srv.Client())
	must(t, e)
	s := testStore(t)
	must(t, s.Seed([]Seed{{Channel: 1, URL: "https://example.com"}}))
	subs, _ := s.Subscriptions()
	must(t, s.Ingest(ctx, subs[0], nil))
	must(t, s.Ingest(ctx, subs[0], []Entry{{ID: "new", Title: "new"}}))
	eng := Engine{Store: s, Fetcher: fixtureFetch{}, Sender: c}
	if eng.Drain(ctx) == nil {
		t.Fatal("lost ack accepted")
	}
	j, e := s.Next()
	must(t, e)
	if j == nil || j.First == 0 {
		t.Fatal("uncertain send lost")
	}
	c.Close()
	c, e = dialConnection(ctx, cfg, "fixture-only-token", srv.Client())
	must(t, e)
	defer c.Close()
	eng.Sender = c
	eng.Now = func() time.Time { return time.Now().Add(31 * time.Second) }
	must(t, eng.Drain(ctx))
	f.mu.Lock()
	n := len(f.Posts)
	f.mu.Unlock()
	if n != 1 {
		t.Fatalf("retry duplicated: %d", n)
	}
	j, e = s.Next()
	must(t, e)
	if j != nil {
		t.Fatal("ack not committed")
	}
}
func TestDemo(t *testing.T) {
	var b bytes.Buffer
	must(t, Demo(context.Background(), &b))
	if !strings.Contains(b.String(), "pending=0 blocked=0 delivered=2") {
		t.Fatal(b.String())
	}
}
func TestConfigStrict(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	base := "server: https://chat.example.com\norigin: https://chat.example.com\nusername: bot\ntoken_file: token\nchannels: [1]\nadmin_ids: [0]\npoll_interval: 5m\nfeeds:\n  - channel: 1\n    url: https://example.com/feed\n"
	must(t, os.WriteFile(p, []byte(base), 0600))
	c, e := LoadConfig(p)
	must(t, e)
	if c.Poll != 5*time.Minute {
		t.Fatal(c.Poll)
	}
	for _, suffix := range []string{"unknown: true\n", "---\nserver: bad\n"} {
		must(t, os.WriteFile(p, []byte(base+suffix), 0600))
		if _, e = LoadConfig(p); e == nil {
			t.Fatal("invalid YAML accepted")
		}
	}
}
func TestProtocolHelpers(t *testing.T) {
	if _, e := ints([]byte{0xff}); e == nil {
		t.Fatal("malformed protobuf accepted")
	}
	b := strfield(intfield(nil, 1, 42), 2, "name")
	v, e := ints(b)
	must(t, e)
	if v[1] != 42 {
		t.Fatal(v)
	}

}
func FuzzParse(f *testing.F) {
	f.Add([]byte(`<rss><channel><item><title>x</title></item></channel></rss>`))
	f.Add([]byte(`<feed/>`))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 65536 {
			return
		}
		Parse(b, "https://example.com")
	})
}

func TestActualErrorShapeAndVoiceHandshake(t *testing.T) {
	f := &Fixture{Posts: map[string]Job{}, Reject: true, Voice: true}
	srv := httptest.NewServer(http.HandlerFunc(f.Handler))
	defer srv.Close()
	c, e := dialConnection(context.Background(), Config{Server: srv.URL, Origin: srv.URL, Username: "fixture", Channels: []int64{1}}, "fixture-only-token", srv.Client())
	must(t, e)
	defer c.Close()
	e = c.Send(context.Background(), Job{Nonce: "same-nonce", Channel: 1, Payload: Payload{Text: "test"}})
	if !errors.Is(e, ErrRejected) {
		t.Fatalf("actual string error lost: %v", e)
	}
	select {
	case <-c.Done():
		t.Fatal("rejection incorrectly closed connection")
	default:
	}
}
func TestIncomingLiveCommandsOnly(t *testing.T) {
	admin := uint32(7)
	self := uint32(42)
	f := &Fixture{Posts: map[string]Job{}, Events: []Event{{Type: "history", Message: Message{ID: 1, Channel: 1, AuthorID: &admin, Verified: true, Text: "!feed help"}}, {Type: "message-edited", Message: Message{ID: 2, Channel: 1, AuthorID: &admin, Verified: true, Text: "!feed help"}}, {Type: "message", Message: Message{ID: 3, Channel: 1, AuthorID: &self, Verified: true, Text: "!feed help"}}, {Type: "message", Message: Message{ID: 4, Channel: 1, AuthorID: &admin, Verified: true, Text: "!feed help"}}}}
	srv := httptest.NewServer(http.HandlerFunc(f.Handler))
	defer srv.Close()
	c, e := dialConnection(context.Background(), Config{Server: srv.URL, Origin: srv.URL, Username: "fixture", Channels: []int64{1}}, "fixture-only-token", srv.Client())
	must(t, e)
	defer c.Close()
	select {
	case m := <-c.Commands:
		if m.ID != 4 {
			t.Fatal("history/edit/self executed", m)
		}
	case <-time.After(time.Second):
		t.Fatal("live message not received")
	}
	select {
	case m := <-c.Commands:
		t.Fatal("unexpected extra command", m)
	case <-time.After(20 * time.Millisecond):
	}
}
func TestPreSendExpiredUploadRefresh(t *testing.T) {
	f := &Fixture{Posts: map[string]Job{}}
	srv := httptest.NewServer(http.HandlerFunc(f.Handler))
	defer srv.Close()
	c, e := dialConnection(context.Background(), Config{Server: srv.URL, Origin: srv.URL, Username: "fixture", Channels: []int64{1}}, "fixture-only-token", srv.Client())
	must(t, e)
	defer c.Close()
	p := filepath.Join(t.TempDir(), "db")
	s, e := OpenStore(p)
	must(t, e)
	must(t, s.Seed([]Seed{{Channel: 1, URL: "https://example.com"}}))
	subs, e := s.Subscriptions()
	must(t, e)
	must(t, s.Ingest(context.Background(), subs[0], nil))
	must(t, s.Ingest(context.Background(), subs[0], []Entry{{ID: "new", Title: "image", Images: []string{"https://example.com/image"}}}))
	j, e := s.Next()
	must(t, e)
	j.Payload.Uploads = []string{"expired-upload"}
	j.Payload.PreparedAt = 1000
	must(t, s.SavePayload(j))
	saved := j.Nonce
	must(t, s.Close())
	s, e = OpenStore(p)
	must(t, e)
	defer s.Close()
	eng := Engine{Store: s, Sender: c, Fetcher: fixtureFetch{"https://example.com/image": fixturePNG()}, Now: func() time.Time { return time.Unix(1700, 0) }}
	must(t, eng.Drain(context.Background()))
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Uploads != 1 || f.Posts[saved].Payload.Uploads[0] != "fixture-image-1" {
		t.Fatal("expired pre-send upload not refreshed")
	}
}
func TestUncertainPayloadNeverChanges(t *testing.T) {
	s := testStore(t)
	must(t, s.Seed([]Seed{{Channel: 1, URL: "https://example.com"}}))
	subs, _ := s.Subscriptions()
	must(t, s.Ingest(context.Background(), subs[0], nil))
	must(t, s.Ingest(context.Background(), subs[0], []Entry{{ID: "new", Title: "image", Images: []string{"https://example.com/image"}}}))
	j, _ := s.Next()
	j.Payload.Uploads = []string{"consumed-key"}
	j.Payload.PreparedAt = 1000
	must(t, s.SavePayload(j))
	must(t, s.Prepare(j, time.Unix(1000, 0)))
	sender := &recordSender{}
	eng := Engine{Store: s, Sender: sender, Fetcher: fixtureFetch{}, Now: func() time.Time { return time.Unix(1700, 0) }}
	must(t, eng.Drain(context.Background()))
	if sender.uploads != 0 || len(sender.sent) != 1 || sender.sent[0].Payload.Uploads[0] != "consumed-key" || sender.sent[0].Nonce != j.Nonce {
		t.Fatal("uncertain payload changed")
	}
}

type recordSender struct {
	uploads int
	sent    []Job
}

func (r *recordSender) Send(_ context.Context, j Job) error { r.sent = append(r.sent, j); return nil }
func (r *recordSender) Upload(_ context.Context, _ []byte) (string, error) {
	r.uploads++
	return "new", nil
}
func TestOutboxResolutionAndClockRollback(t *testing.T) {
	s := testStore(t)
	must(t, s.Seed([]Seed{{Channel: 1, URL: "https://example.com"}}))
	subs, _ := s.Subscriptions()
	must(t, s.Ingest(context.Background(), subs[0], nil))
	must(t, s.Ingest(context.Background(), subs[0], []Entry{{ID: "x", Title: "x"}}))
	j, _ := s.Next()
	must(t, s.Prepare(j, time.Unix(1000, 0)))
	if s.Prepare(j, time.Unix(999, 0)) == nil {
		t.Fatal("backwards clock retried")
	}
	jobs, e := s.Inspect()
	must(t, e)
	if len(jobs) != 1 || jobs[0].State != "blocked" {
		t.Fatal(jobs)
	}
	if s.Resolve(j.ID, "pending") == nil {
		t.Fatal("unsafe retry allowed")
	}
	must(t, s.Resolve(j.ID, "discarded"))
	jobs, e = s.Inspect()
	must(t, e)
	if len(jobs) != 0 {
		t.Fatal("resolution not durable")
	}
}
func TestCapacityRollback(t *testing.T) {
	s := testStore(t)
	must(t, s.Seed([]Seed{{Channel: 1, URL: "https://example.com"}}))
	subs, _ := s.Subscriptions()
	must(t, s.Ingest(context.Background(), subs[0], nil))
	_, e := s.DB.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<10000) INSERT INTO outbox(channel,nonce,payload) SELECT 1,'n'||x,'{}' FROM n`)
	must(t, e)
	if s.Ingest(context.Background(), subs[0], []Entry{{ID: "fresh", Title: "fresh"}}) == nil {
		t.Fatal("capacity ignored")
	}
	var n int
	must(t, s.DB.QueryRow("SELECT count(*) FROM seen WHERE item='fresh'").Scan(&n))
	if n != 0 {
		t.Fatal("failed enqueue advanced seen state")
	}
}

func TestWebPRaster(t *testing.T) {
	b, e := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	must(t, e)
	_, e = Raster(b)
	must(t, e)
}
func TestSeedLimit(t *testing.T) {
	s := testStore(t)
	var seeds []Seed
	for i := 0; i < 100; i++ {
		seeds = append(seeds, Seed{Channel: int64(i + 1), URL: "https://example.com"})
	}
	must(t, s.Seed(seeds))
	if s.Seed([]Seed{{Channel: 101, URL: "https://example.com"}}) == nil {
		t.Fatal("seed bypassed cap")
	}
	must(t, s.Seed(seeds))
}
func TestThreeImagesWire(t *testing.T) {
	f := &Fixture{Posts: map[string]Job{}}
	srv := httptest.NewServer(http.HandlerFunc(f.Handler))
	defer srv.Close()
	c, e := dialConnection(context.Background(), Config{Server: srv.URL, Origin: srv.URL, Username: "fixture", Channels: []int64{1}}, "fixture-only-token", srv.Client())
	must(t, e)
	defer c.Close()
	s := testStore(t)
	must(t, s.Seed([]Seed{{Channel: 1, URL: "https://example.com"}}))
	subs, _ := s.Subscriptions()
	must(t, s.Ingest(context.Background(), subs[0], nil))
	must(t, s.Ingest(context.Background(), subs[0], []Entry{{ID: "x", Title: "x", Images: []string{"https://example.com/1", "https://example.com/2", "https://example.com/3", "https://example.com/4"}}}))
	eng := Engine{Store: s, Sender: c, Fetcher: fixtureFetch{"https://example.com/1": fixturePNG(), "https://example.com/2": fixturePNG(), "https://example.com/3": fixturePNG()}}
	must(t, eng.Drain(context.Background()))
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Uploads != 3 {
		t.Fatal("image cap", f.Uploads)
	}
	for _, j := range f.Posts {
		if len(j.Payload.Uploads) != 3 || j.Payload.Uploads[0] == j.Payload.Uploads[1] {
			t.Fatal("multiple attachments not preserved")
		}
	}
}

func TestPasswordLoginCachedAndDiscovery(t *testing.T) {
	p := filepath.Join(t.TempDir(), "password")
	must(t, os.WriteFile(p, []byte("fixture-password\n"), 0600))
	f := &Fixture{Posts: map[string]Job{}}
	var logins atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/login" {
			logins.Add(1)
			var v map[string]string
			must(t, json.NewDecoder(r.Body).Decode(&v))
			if r.Method != "POST" || v["name"] != "fixture-bot" || v["password"] != "fixture-password" || r.Header.Get("X-Kallisti-Session") != "token" || r.Header.Get("Authorization") != "" || r.Header.Get("Origin") != srv.URL {
				t.Error("incorrect login contract")
			}
			fmt.Fprint(w, `{"name":"fixture-bot","token":"fixture-only-token"}`)
			return
		}
		f.Handler(w, r)
	}))
	defer srv.Close()
	d := NewDialer(Config{Server: srv.URL, Origin: srv.URL, Username: "fixture-bot", PasswordFile: p})
	d.client = srv.Client()
	for i := 0; i < 2; i++ {
		c, e := d.Connect(context.Background())
		must(t, e)
		v := c.Discovery()
		if v.BotUserID != 42 || len(v.Channels) != 2 || v.Channels[0].Name != "art" || len(v.OnlineRegistered) != 1 || v.OnlineRegistered[0].Name != "fixture-bot" {
			t.Fatalf("discovery %+v", v)
		}
		c.Close()
	}
	if logins.Load() != 1 {
		t.Fatal("session not cached")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Posts) != 0 || f.Uploads != 0 {
		t.Fatal("discovery posted or uploaded")
	}
}
func TestLoginFailuresAndSecretBounds(t *testing.T) {
	p := filepath.Join(t.TempDir(), "password")
	must(t, os.WriteFile(p, []byte("secret-value"), 0600))
	cfg := Config{Server: "https://example.com", Origin: "https://example.com", Username: "fixture", PasswordFile: p}
	for _, response := range []struct {
		status int
		body   string
	}{{401, `secret-value`}, {200, `{"mumble":true}`}, {200, `{"token":"short"}`}, {200, strings.Repeat("x", 32769)}} {
		client := &http.Client{Transport: roundFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: response.status, Body: io.NopCloser(strings.NewReader(response.body)), Header: make(http.Header)}, nil
		})}
		_, e := login(context.Background(), cfg, client)
		if e == nil || strings.Contains(e.Error(), "secret-value") {
			t.Fatalf("invalid login accepted or leaked: %v", e)
		}
	}
	must(t, os.WriteFile(p, bytes.Repeat([]byte("x"), 4097), 0600))
	if _, e := readSecret(p); e == nil {
		t.Fatal("unbounded secret")
	}
	if serverClient().CheckRedirect(&http.Request{}, nil) == nil {
		t.Fatal("credential-bearing redirects accepted")
	}
}
func TestDiscoveryConfigAndExclusiveAuth(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	base := "server: https://example.com\norigin: https://example.com\nusername: fixture\npassword_file: ./password\nchannels: []\n"
	must(t, os.WriteFile(p, []byte(base), 0600))
	if _, e := LoadDiscoveryConfig(p); e != nil {
		t.Fatal(e)
	}
	if _, e := LoadConfig(p); e == nil {
		t.Fatal("normal mode allowed no channels")
	}
	must(t, os.WriteFile(p, []byte(base+"token_file: ./token\n"), 0600))
	if _, e := LoadDiscoveryConfig(p); e == nil {
		t.Fatal("ambiguous credentials accepted")
	}
}

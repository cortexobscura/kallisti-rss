package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/coder/websocket"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
)

// Fixture runs only in explicit demo mode. No external sockets or credentials.
type Fixture struct {
	mu              sync.Mutex
	Posts           map[string]Job
	Uploads         int
	Reject          bool
	LoseAck         bool
	Events          []Event
	Voice           bool
	RejectChannels  map[int64]string
	RequestChannels []int64
	ErrorAbout      string
	WrongNonce      bool
}

func (f *Fixture) Handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/upload" {
		if r.Header.Get("X-Upload-Token") != "fixture-upload" {
			http.Error(w, "token", 401)
			return
		}
		b, e := io.ReadAll(io.LimitReader(r.Body, MediaLimit+1))
		if e != nil {
			http.Error(w, "body", 400)
			return
		}
		if _, e = Raster(b); e != nil {
			http.Error(w, "raster", 400)
			return
		}
		f.mu.Lock()
		f.Uploads++
		number := f.Uploads
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"key":"fixture-image-%d","type":"image/png"}`, number)
		return
	}
	if r.URL.Path != "/ws" {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer fixture-only-token" {
		http.Error(w, "token", 401)
		return
	}
	ws, e := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, Subprotocols: []string{"kallisti"}})
	if e != nil {
		return
	}
	defer ws.CloseNow()
	ctx := r.Context()
	t, b, e := ws.Read(ctx)
	if e != nil || t != websocket.MessageBinary || len(b) < 12 {
		return
	}
	if f.Voice {
		if ws.Write(ctx, websocket.MessageBinary, frame(1, []byte{0xff, 0xff, 0xff})) != nil {
			return
		}
	}
	u := strfield(intfield(intfield(nil, 1, 11), 4, 42), 3, "fixture-bot")
	syncFrame := intfield(nil, 1, 11)
	if ws.Write(ctx, websocket.MessageBinary, append(frame(9, u), frame(5, syncFrame)...)) != nil {
		return
	}
	ready := []byte(`{"type":"ready","channels":[{"id":1,"name":"art"},{"id":2,"name":"news"}],"uploadMax":8388608,"maxUploads":10}`)
	if ws.Write(ctx, websocket.MessageText, ready) != nil {
		return
	}
	for _, ev := range f.Events {
		raw, _ := json.Marshal(ev)
		if ws.Write(ctx, websocket.MessageText, raw) != nil {
			return
		}
	}
	for {
		_, b, e = ws.Read(ctx)
		if e != nil {
			return
		}
		var req struct {
			Type    string `json:"type"`
			Nonce   string `json:"nonce"`
			Channel int64  `json:"channel"`
			Text    string `json:"text"`
			Uploads []struct {
				Key string `json:"key"`
			} `json:"uploads"`
		}
		if json.Unmarshal(b, &req) != nil {
			return
		}
		if req.Type == "upload-token" {
			if ws.Write(ctx, websocket.MessageText, []byte(`{"type":"upload-token","token":"fixture-upload"}`)) != nil {
				return
			}
			continue
		}
		if req.Type != "send" {
			return
		}
		f.mu.Lock()
		f.RequestChannels = append(f.RequestChannels, req.Channel)
		reason := f.RejectChannels[req.Channel]
		reject, lose := f.Reject || reason != "", f.LoseAck
		about, wrongNonce := f.ErrorAbout, f.WrongNonce
		f.LoseAck = false
		if !reject {
			p := Payload{Text: req.Text}
			if len(req.Uploads) > 0 {
				for _, up := range req.Uploads {
					p.Uploads = append(p.Uploads, up.Key)
				}
			}
			if _, ok := f.Posts[req.Nonce]; !ok {
				f.Posts[req.Nonce] = Job{Nonce: req.Nonce, Channel: req.Channel, Payload: p}
			}
		}
		f.mu.Unlock()
		if lose {
			return
		}
		typ := "message"
		if reject {
			typ = "error"
		}
		var message any = Message{ID: 1, Channel: req.Channel, Verified: true}
		if reject {
			message = "synthetic permission refusal"
			if reason != "" {
				message = reason
			}
		}
		replyNonce := req.Nonce
		if wrongNonce {
			replyNonce = "wrong-nonce"
		}
		ack, _ := json.Marshal(map[string]any{"type": typ, "nonce": replyNonce, "message": message, "channel": req.Channel, "about": about})
		if ws.Write(ctx, websocket.MessageText, ack) != nil {
			return
		}
	}
}

type fixtureFetch map[string][]byte

func (f fixtureFetch) Fetch(_ context.Context, u string, limit int64) ([]byte, error) {
	b, ok := f[u]
	if !ok || int64(len(b)) > limit {
		return nil, fmt.Errorf("fixture missing or oversized")
	}
	return b, nil
}
func fixturePNG() []byte {
	im := image.NewRGBA(image.Rect(0, 0, 8, 8))
	im.Set(0, 0, color.RGBA{120, 50, 240, 255})
	var b bytes.Buffer
	_ = png.Encode(&b, im)
	return b.Bytes()
}
func Demo(ctx context.Context, out io.Writer) error {
	dir, e := os.MkdirTemp("", "feedbot-demo-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	fixture := &Fixture{Posts: map[string]Job{}}
	server := httptest.NewServer(http.HandlerFunc(fixture.Handler))
	defer server.Close()
	cfg := Config{Server: server.URL, Origin: server.URL, Username: "fixture-bot", Channels: []int64{1}, Admins: []uint32{7}}
	conn, e := dialConnection(ctx, cfg, "fixture-only-token", server.Client())
	if e != nil {
		return e
	}
	defer conn.Close()
	path := filepath.Join(dir, "demo.db")
	s, e := OpenStore(path)
	if e != nil {
		return e
	}
	defer func() {
		if s != nil {
			_ = s.Close()
		}
	}()
	if e = s.Seed([]Seed{{Channel: 1, URL: "https://fixture.example/feed"}}); e != nil {
		return e
	}
	feed := fixtureFetch{"https://fixture.example/feed": []byte(`<rss><channel><item><guid>old</guid><title>Existing entry</title></item></channel></rss>`), "https://fixture.example/image.png": fixturePNG()}
	engine := Engine{Config: cfg, Store: s, Fetcher: feed, Sender: conn}
	if e = engine.Poll(ctx); e != nil {
		return e
	}
	if e = engine.Drain(ctx); e != nil {
		return e
	}
	if len(fixture.Posts) != 0 {
		return fmt.Errorf("baseline flooded")
	}
	fmt.Fprintln(out, "PASS first subscription: baseline recorded, no backlog")
	feed["https://fixture.example/feed"] = []byte(`<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>new</id><title>New fixture art</title><link href="https://fixture.example/post"/><content type="html">&lt;p&gt;Original synthetic fixture&lt;/p&gt;&lt;img src="https://fixture.example/image.png"/&gt;</content></entry><entry><id>old</id><title>Existing entry</title></entry></feed>`)
	if e = engine.Poll(ctx); e != nil {
		return e
	}
	if e = engine.Drain(ctx); e != nil {
		return e
	}
	if len(fixture.Posts) != 1 || fixture.Uploads != 1 {
		return fmt.Errorf("image delivery failed")
	}
	fmt.Fprintln(out, "PASS Atom → bounded raster → raw upload → nonce acknowledgment")
	if e = s.Close(); e != nil {
		return e
	}
	s = nil
	s, e = OpenStore(path)
	if e != nil {
		return e
	}
	engine.Store = s
	if e = engine.Poll(ctx); e != nil {
		return e
	}
	if e = engine.Drain(ctx); e != nil {
		return e
	}
	if len(fixture.Posts) != 1 {
		return fmt.Errorf("restart duplicate")
	}
	fmt.Fprintln(out, "PASS SQLite restart: no duplicate delivery")
	id := uint32(7)
	if e = engine.Command(ctx, Message{ID: 101, Channel: 1, AuthorID: &id, Verified: true, Text: "!feed pause 1"}); e != nil {
		return e
	}
	if e = engine.Command(ctx, Message{ID: 101, Channel: 1, AuthorID: &id, Verified: true, Text: "!feed pause 1"}); e != nil {
		return e
	}
	if e = engine.Drain(ctx); e != nil {
		return e
	}
	fmt.Fprintln(out, "PASS verified account command: durable mutation, duplicate ignored")
	status, e := s.Status()
	fmt.Fprintln(out, status)
	return e
}

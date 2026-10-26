package bot

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/encoding/protowire"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

type Event struct {
	Type         string    `json:"type"`
	Nonce        string    `json:"nonce"`
	Token        string    `json:"token"`
	Message      Message   `json:"message"`
	Channels     []Channel `json:"channels"`
	UploadMax    int64     `json:"uploadMax"`
	MaxUploads   int       `json:"maxUploads"`
	About        string    `json:"about,omitempty"`
	ErrorChannel int64     `json:"channel,omitempty"`
	rejection    *DeliveryFailure
}
type Connection struct {
	ws          *websocket.Conn
	cfg         Config
	client      *http.Client
	events      chan Event
	Commands    chan Message
	done        chan struct{}
	mu          sync.Mutex
	self        uint32
	UploadMax   int64
	maxUploads  int
	nextRequest time.Time
	discovery   Discovery
}

func frame(t uint16, b []byte) []byte {
	if len(b) > 8<<20 {
		panic("internal frame limit")
	}
	v := make([]byte, 6+len(b))
	binary.BigEndian.PutUint16(v, t)
	// #nosec G115 -- length was checked against 8 MiB above, well below MaxUint32.
	binary.BigEndian.PutUint32(v[2:], uint32(len(b)))
	copy(v[6:], b)
	return v
}
func strfield(b []byte, n protowire.Number, s string) []byte {
	b = protowire.AppendTag(b, n, protowire.BytesType)
	return protowire.AppendString(b, s)
}
func intfield(b []byte, n protowire.Number, v uint64) []byte {
	b = protowire.AppendTag(b, n, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}
func ints(b []byte) (map[protowire.Number]uint64, error) {
	m := map[protowire.Number]uint64{}
	for len(b) > 0 {
		n, t, k := protowire.ConsumeTag(b)
		if k < 0 {
			return nil, errors.New("invalid protobuf")
		}
		b = b[k:]
		if t == protowire.VarintType {
			v, k := protowire.ConsumeVarint(b)
			if k < 0 {
				return nil, errors.New("invalid protobuf")
			}
			m[n] = v
			b = b[k:]
		} else {
			k := protowire.ConsumeFieldValue(n, t, b)
			if k < 0 {
				return nil, errors.New("invalid protobuf")
			}
			b = b[k:]
		}
	}
	return m, nil
}

func dialConnection(ctx context.Context, cfg Config, token string, client *http.Client) (*Connection, error) {
	u := strings.TrimRight(cfg.Server, "/")
	wsURL := "wss" + strings.TrimPrefix(u, "https") + "/ws"
	if strings.HasPrefix(u, "http://") {
		wsURL = "ws" + strings.TrimPrefix(u, "http") + "/ws"
	} // only fixture callers bypass validated Config
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	h.Set("Origin", cfg.Origin)
	handshake, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ws, _, e := websocket.Dial(handshake, wsURL, &websocket.DialOptions{HTTPClient: client, HTTPHeader: h, Subprotocols: []string{"kallisti"}})
	if e != nil {
		return nil, errors.New("server websocket connection failed")
	}
	ws.SetReadLimit(8 << 20)
	fail := func(err error) (*Connection, error) { _ = ws.CloseNow(); return nil, err }
	v := intfield(nil, 1, 0x10500)
	v = strfield(v, 2, "kallisti-rss")
	a := strfield(nil, 1, cfg.Username)
	a = intfield(a, 5, 1)
	if e = ws.Write(handshake, websocket.MessageBinary, append(frame(0, v), frame(2, a)...)); e != nil {
		return fail(errors.New("handshake write failed"))
	}
	ready, synced := false, false
	var session uint64
	users := map[uint64]uint32{}
	names := map[uint64]string{}
	var pending []byte
	var r Event
	var readyEvent Event
	for !ready || !synced {
		typ, b, e := ws.Read(handshake)
		if e != nil {
			return fail(errors.New("handshake failed or timed out"))
		}
		if typ == websocket.MessageText {
			if e = json.Unmarshal(b, &r); e != nil {
				return fail(errors.New("invalid sidecar handshake"))
			}
			if r.Type == "ready" {
				ready = true
				readyEvent = r
			}
		} else {
			pending = append(pending, b...)
			for len(pending) >= 6 {
				size := binary.BigEndian.Uint32(pending[2:])
				if size > 8<<20 {
					return fail(errors.New("Mumble frame too large"))
				}
				if len(pending) < 6+int(size) {
					break
				}
				t := binary.BigEndian.Uint16(pending)
				var fields map[protowire.Number]uint64
				if t == 5 || t == 9 {
					var er error
					fields, er = ints(pending[6 : 6+size])
					if er != nil {
						return fail(er)
					}
				}
				switch t {
				case 4:
					return fail(errors.New("server rejected authentication"))
				case 5:
					session = fields[1]
					synced = true
				case 9:
					if id, ok := fields[4]; ok {
						if id > math.MaxUint32 {
							return fail(errors.New("invalid registered user ID"))
						}
						users[fields[1]] = uint32(id)
						names[fields[1]] = protoName(pending[6 : 6+size])
					}
				}
				pending = pending[6+size:]
			}
		}
	}
	self, ok := users[session]
	if !ok {
		return fail(errors.New("server did not verify registered bot identity"))
	}
	for _, id := range cfg.Channels {
		found := false
		for _, ch := range readyEvent.Channels {
			if ch.ID == id {
				found = true
			}
		}
		if !found {
			return fail(errors.New("configured text channel not visible"))
		}
	}
	c := &Connection{ws: ws, cfg: cfg, client: client, events: make(chan Event, 32), Commands: make(chan Message, 256), done: make(chan struct{}), self: self, UploadMax: readyEvent.UploadMax, maxUploads: readyEvent.MaxUploads}
	c.discovery = Discovery{BotUserID: self, Channels: readyEvent.Channels}
	for session, id := range users {
		c.discovery.OnlineRegistered = append(c.discovery.OnlineRegistered, RegisteredUser{Session: session, ID: id, Name: names[session]})
	}
	sort.Slice(c.discovery.OnlineRegistered, func(i, j int) bool { return c.discovery.OnlineRegistered[i].ID < c.discovery.OnlineRegistered[j].ID })
	go c.read(ctx)
	return c, nil
}
func (c *Connection) read(ctx context.Context) {
	defer close(c.done)
	defer c.ws.CloseNow()
	for {
		t, b, e := c.ws.Read(ctx)
		if e != nil {
			return
		}
		if t != websocket.MessageText {
			continue
		}
		var v Event
		if json.Unmarshal(b, &v) != nil {
			return
		}
		if v.Type == "message" && v.Nonce == "" {
			if v.Message.AuthorID != nil && *v.Message.AuthorID == c.self {
				continue
			}
			if strings.HasPrefix(v.Message.Text, "!feed ") {
				select {
				case c.Commands <- v.Message:
				default:
					return
				}
			}
			continue
		}
		if v.Nonce != "" || v.Type == "upload-token" || v.Type == "error" {
			select {
			case c.events <- v:
			default:
				return
			}
		}
	}
}
func (c *Connection) Close()                { _ = c.ws.CloseNow() }
func (c *Connection) Done() <-chan struct{} { return c.done }
func (c *Connection) rpc(ctx context.Context, req any, match func(Event) bool) (Event, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if delay := time.Until(c.nextRequest); delay > 0 {
		select {
		case <-ctx.Done():
			return Event{}, globalFailure("request_canceled", "send")
		case <-time.After(delay):
		}
	}
	c.nextRequest = time.Now().Add(250 * time.Millisecond)
	b, e := json.Marshal(req)
	if e != nil {
		return Event{}, e
	}
	if e = c.ws.Write(ctx, websocket.MessageText, b); e != nil {
		return Event{}, globalFailure("write_failed", "send")
	}
	for {
		select {
		case <-ctx.Done():
			c.Close()
			return Event{}, globalFailure("ack_timeout", "acknowledgment")
		case <-c.done:
			return Event{}, globalFailure("disconnected", "acknowledgment")
		case v := <-c.events:
			if match(v) {
				if v.Type == "error" {
					if v.rejection != nil {
						return v, v.rejection
					}
					return v, classifyRejection("", "", 0)
				}
				return v, nil
			}
		}
	}
}
func (c *Connection) Send(ctx context.Context, j Job) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	req := map[string]any{"type": "send", "channel": j.Channel, "text": j.Payload.Text, "nonce": j.Nonce}

	if len(j.Payload.Uploads) > 0 {
		var uploads []map[string]string
		for _, key := range j.Payload.Uploads {
			uploads = append(uploads, map[string]string{"key": key, "description": "Image from subscribed feed"})
		}
		req["uploads"] = uploads
	}

	_, e := c.rpc(ctx, req, func(v Event) bool { return v.Nonce == j.Nonce && (v.Type == "message" || v.Type == "error") })
	return scopeRejection(e, j)
}
func (c *Connection) Upload(ctx context.Context, b []byte) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(b) > MediaLimit || int64(len(b)) > c.UploadMax {
		return "", errors.New("server image size limit")
	}
	ev, e := c.rpc(ctx, map[string]any{"type": "upload-token", "count": 1}, func(v Event) bool { return v.Type == "upload-token" || v.Type == "error" && v.Nonce == "" })
	if e != nil {
		return "", e
	}
	if ev.Token == "" || len(ev.Token) > 4096 {
		return "", errors.New("invalid upload token")
	}
	r, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.cfg.Server, "/")+"/api/upload", bytes.NewReader(b))
	if e != nil {
		return "", e
	}
	r.Header.Set("X-Upload-Token", ev.Token)
	r.Header.Set("X-Filename", url.QueryEscape("feed-image.png"))
	r.Header.Set("Content-Type", "image/png")
	r.Header.Set("Origin", c.cfg.Origin)
	res, e := c.client.Do(r)
	if e != nil {
		return "", errors.New("image upload failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 && res.StatusCode != 201 {
		return "", &Diagnostic{Class: "upload_http_status", Status: res.StatusCode, Reason: "server refused image upload"}
	}
	var v struct {
		Key  string `json:"key"`
		Type string `json:"type"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 32768)).Decode(&v) != nil || v.Key == "" || len(v.Key) > 256 || v.Type != "image/png" {
		return "", errors.New("invalid upload result")
	}
	return v.Key, nil
}

func (c *Connection) UserID() uint32 { return c.self }

func (c *Connection) MaxImages() int {
	n := c.maxUploads
	if n < 0 {
		return 0
	}
	if n > 10 {
		return 10
	}
	return n
}

var ErrRejected = errors.New("server refused request")

// Error envelopes use a string message; delivered messages use an object.
func (e *Event) UnmarshalJSON(b []byte) error {
	type alias Event
	var wire struct {
		*alias
		Message json.RawMessage `json:"message"`
	}
	*e = Event{}
	wire.alias = (*alias)(e)
	if err := json.Unmarshal(b, &wire); err != nil {
		return err
	}
	if e.Type == "error" {
		var reason string
		if len(wire.Message) > 0 && len(wire.Message) <= 1024 {
			if err := json.Unmarshal(wire.Message, &reason); err != nil {
				return err
			}
		}
		e.rejection = classifyRejection(reason, e.About, e.ErrorChannel)
		e.About = ""
		return nil
	}
	if len(wire.Message) > 0 && wire.Message[0] == '{' {
		return json.Unmarshal(wire.Message, &e.Message)
	}
	return nil
}

type Channel struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}
type RegisteredUser struct {
	Session uint64 `json:"session"`
	ID      uint32 `json:"registered_id"`
	Name    string `json:"name"`
}
type Discovery struct {
	BotUserID        uint32           `json:"bot_registered_id"`
	Channels         []Channel        `json:"text_channels"`
	OnlineRegistered []RegisteredUser `json:"online_registered_users"`
}

func (c *Connection) Discovery() Discovery { return c.discovery }
func protoName(b []byte) string {
	for len(b) > 0 {
		n, t, k := protowire.ConsumeTag(b)
		if k < 0 {
			return ""
		}
		b = b[k:]
		if n == 3 && t == protowire.BytesType {
			s, k := protowire.ConsumeString(b)
			if k < 0 {
				return ""
			}
			return cut(s, 128)
		}
		k = protowire.ConsumeFieldValue(n, t, b)
		if k < 0 {
			return ""
		}
		b = b[k:]
	}
	return ""
}

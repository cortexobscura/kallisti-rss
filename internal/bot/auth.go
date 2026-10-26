package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Dialer retains a password-login session only in memory across reconnects.
// Local credentials are reread on a new login; no credential files are written.
type Dialer struct {
	cfg     Config
	client  *http.Client
	mu      sync.Mutex
	token   string
	expires time.Time
}

func NewDialer(c Config) *Dialer { return &Dialer{cfg: c, client: serverClient()} }
func serverClient() *http.Client {
	return &http.Client{Timeout: 25 * time.Second, Transport: &http.Transport{Proxy: nil, ResponseHeaderTimeout: 10 * time.Second, TLSHandshakeTimeout: 10 * time.Second, MaxResponseHeaderBytes: 32768}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("server redirects refused") }}
}
func readSecret(path string) (string, error) {
	// #nosec G304 -- secret path comes only from explicit local operator configuration.
	f, e := os.Open(path)
	if e != nil {
		return "", errors.New("cannot read configured secret file")
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 4097))
	if e != nil || len(b) > 4096 {
		return "", errors.New("invalid secret file size")
	}
	s := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	if s == "" || strings.ContainsAny(s, "\r\n") {
		return "", errors.New("secret must be one nonempty line")
	}
	return s, nil
}
func login(ctx context.Context, c Config, client *http.Client) (string, error) {
	password, e := readSecret(c.PasswordFile)
	if e != nil {
		return "", e
	}
	b, e := json.Marshal(map[string]string{"name": c.Username, "password": password})
	if e != nil || len(b) > 4096 {
		return "", errors.New("login credentials exceed server bounds")
	}
	r, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.Server, "/")+"/api/login", bytes.NewReader(b))
	if e != nil {
		return "", errors.New("invalid login endpoint")
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Kallisti-Session", "token")
	r.Header.Set("Origin", c.Origin)
	res, e := client.Do(r)
	if e != nil {
		return "", errors.New("login request failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", errors.New("login refused; check account credentials and server policy")
	}
	data, e := io.ReadAll(io.LimitReader(res.Body, 32769))
	if e != nil || len(data) > 32768 {
		return "", errors.New("invalid login response")
	}
	var v struct {
		Token  string `json:"token"`
		Mumble bool   `json:"mumble"`
	}
	if json.Unmarshal(data, &v) != nil || v.Mumble || !validToken(v.Token) {
		return "", errors.New("login did not return a supported web-account session token")
	}
	return v.Token, nil
}
func validToken(s string) bool {
	return len(s) >= 16 && len(s) <= 4096 && !strings.ContainsAny(s, " \t\r\n")
}
func (d *Dialer) Connect(ctx context.Context) (*Connection, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var e error
	if d.cfg.TokenFile != "" {
		d.token, e = readSecret(d.cfg.TokenFile)
		if e != nil {
			return nil, e
		}
		if !validToken(d.token) {
			return nil, errors.New("invalid session token file")
		}
	} else if d.token == "" || time.Now().After(d.expires) {
		d.token, e = login(ctx, d.cfg, d.client)
		if e != nil {
			return nil, e
		}
		d.expires = time.Now().Add(29 * 24 * time.Hour)
	}
	return dialConnection(ctx, d.cfg, d.token, d.client)
}

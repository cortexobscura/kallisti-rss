package bot

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const FeedLimit = 4 << 20
const MediaLimit = 8 << 20

func ValidateURL(raw string, allowHTTP bool) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || len(raw) > 2048 || u == nil {
		return nil, errors.New("invalid URL")
	}
	if (u.Scheme != "https" && !(allowHTTP && u.Scheme == "http")) || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("URL must be public HTTPS without credentials or fragment")
	}
	if p := u.Port(); p != "" && p != "443" && !(allowHTTP && p == "80") {
		return nil, errors.New("feed port not allowed")
	}
	if a, e := netip.ParseAddr(u.Hostname()); e == nil && !PublicIP(a) {
		return nil, errors.New("nonpublic address")
	}
	if strings.EqualFold(u.Hostname(), "localhost") || strings.HasSuffix(strings.ToLower(u.Hostname()), ".local") {
		return nil, errors.New("nonpublic hostname")
	}
	return u, nil
}

var reserved = []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "::/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "ff00::/8"}

func PublicIP(a netip.Addr) bool {
	a = a.Unmap()
	if a.Is6() && !netip.MustParsePrefix("2000::/3").Contains(a) {
		return false
	}
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range reserved {
		if netip.MustParsePrefix(p).Contains(a) {
			return false
		}
	}
	return true
}

type Fetcher interface {
	Fetch(context.Context, string, int64) ([]byte, error)
}
type PublicFetcher struct {
	Client    *http.Client
	AllowHTTP bool
}

func NewFetcher(allow bool) *PublicFetcher {
	f := &PublicFetcher{AllowHTTP: allow}
	tr := &http.Transport{Proxy: nil, DisableCompression: true, MaxIdleConns: 8, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 32768}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if e != nil || len(ips) == 0 {
			return nil, diag("dns_error", "hostname lookup failed")
		}
		for _, a := range ips {
			if !PublicIP(a) {
				return nil, diag("address_blocked", "DNS resolved a nonpublic address")
			}
		}
		d := net.Dialer{Timeout: 8 * time.Second}
		return d.DialContext(ctx, "tcp", net.JoinHostPort(ips[0].String(), port))
	}
	f.Client = &http.Client{Transport: tr, Timeout: 20 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return diag("redirect_limit", "more than three redirects")
		}
		_, e := ValidateURL(r.URL.String(), allow)
		if e != nil {
			return diag("redirect_blocked", "redirect target violates public URL policy")
		}
		return nil
	}}
	return f
}
func (f *PublicFetcher) Fetch(ctx context.Context, raw string, limit int64) ([]byte, error) {
	if _, e := ValidateURL(raw, f.AllowHTTP); e != nil {
		return nil, diag("url_policy", "URL violates public HTTP(S) policy")
	}
	r, e := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if e != nil {
		return nil, diag("url_policy", "invalid HTTP request")
	}
	r.Header.Set("User-Agent", "kallisti-rss/0.1 (+RSS/Atom)")
	res, e := f.Client.Do(r)
	if e != nil {
		return nil, transportDiagnostic(e)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		reason := "upstream returned non-200 status"
		switch res.StatusCode {
		case 401, 403:
			reason = "upstream denied access; verify public feed availability (no login bypass)"
		case 404, 410:
			reason = "feed endpoint not found or removed"
		case 429:
			reason = "upstream rate limited this client"
		}
		return nil, &Diagnostic{Class: "http_status", Status: res.StatusCode, Reason: reason}
	}
	if res.ContentLength > limit {
		return nil, diag("response_too_large", "response exceeds configured byte limit")
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if e != nil {
		var d *Diagnostic
		if classified := transportDiagnostic(e); errors.As(classified, &d) && (d.Class == "timeout" || d.Class == "canceled") {
			return nil, classified
		}
		return nil, diag("response_read", "HTTP response body could not be read")
	}
	if int64(len(b)) > limit {
		return nil, diag("response_too_large", "response exceeds configured byte limit")
	}
	return b, nil
}

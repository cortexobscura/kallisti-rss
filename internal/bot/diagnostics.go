package bot

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"time"
)

// Diagnostic contains only controlled labels and numeric HTTP status. Never
// retain wrapped transport errors: they can contain query tokens and userinfo.
type Diagnostic struct {
	Class  string
	Status int
	Reason string
}

func (d *Diagnostic) Error() string {
	if d.Status != 0 {
		return fmt.Sprintf("%s: HTTP %d (%s)", d.Class, d.Status, d.Reason)
	}
	return d.Class + ": " + d.Reason
}
func diag(class, reason string) error { return &Diagnostic{Class: class, Reason: reason} }
func transportDiagnostic(err error) error {
	var d *Diagnostic
	if errors.As(err, &d) {
		return d
	}
	if errors.Is(err, context.Canceled) {
		return diag("canceled", "request canceled")
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout() {
		return diag("timeout", "request deadline exceeded")
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return diag("dns_error", "hostname lookup failed")
	}
	var cert *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	var record tls.RecordHeaderError
	if errors.As(err, &cert) || errors.As(err, &unknown) || errors.As(err, &host) || errors.As(err, &invalid) || errors.As(err, &record) {
		return diag("tls_error", "TLS handshake or certificate verification failed")
	}
	return diag("network_error", "connection or HTTP transport failed")
}
func parserDiagnostic(err error) error {
	// Parse emits these fixed messages. Unknown implementations/errors are never
	// reflected into logs, even if a future parser includes source XML or URLs.
	switch err.Error() {
	case "not RSS or Atom", "invalid XML", "invalid entry", "XML nesting limit", "XML directives are not allowed", "entry limit", "feed too large":
		return diag("parse_error", err.Error())
	}
	return diag("parse_error", "feed parsing failed")
}
func fetchEntries(ctx context.Context, f Fetcher, raw string) ([]Entry, error) {
	b, err := f.Fetch(ctx, raw, FeedLimit)
	if err != nil {
		return nil, transportDiagnostic(err)
	}
	v, err := Parse(b, raw)
	if err != nil {
		return nil, parserDiagnostic(err)
	}
	return v, nil
}
func feedFields(raw string) []any {
	host := "invalid"
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	return []any{"feed_host", host, "feed_key", hexDigest(raw)[:12]}
}
func errorFields(err error) []any {
	var d *Diagnostic
	if !errors.As(err, &d) {
		return []any{"error_class", "internal_error", "reason", "operation failed"}
	}
	out := []any{"error_class", d.Class, "reason", d.Reason}
	if d.Status != 0 {
		out = append(out, "http_status", d.Status)
	}
	return out
}
func pollInterval(c Config) time.Duration {
	if c.Poll == 0 {
		return 5 * time.Minute
	}
	return c.Poll
}
func seedIndex(c Config, ch int64, raw string) int {
	for i, s := range c.Feeds {
		if s.Channel == ch && s.URL == raw {
			return i + 1
		}
	}
	return 0
}

type SubscriptionView struct {
	ID          int64  `json:"subscription"`
	Channel     int64  `json:"channel"`
	YAMLFeed    int    `json:"yaml_feed"`
	Host        string `json:"feed_host"`
	Key         string `json:"feed_key"`
	Initialized bool   `json:"initialized"`
	Paused      bool   `json:"paused"`
	MaxImages   int    `json:"max_images"`
}

func SubscriptionViews(c Config, subs []Subscription) []SubscriptionView {
	out := make([]SubscriptionView, 0, len(subs))
	for _, s := range subs {
		fields := feedFields(s.URL)
		out = append(out, SubscriptionView{ID: s.ID, Channel: s.Channel, YAMLFeed: seedIndex(c, s.Channel, s.URL), Host: fields[1].(string), Key: fields[3].(string), Initialized: s.Initialized, Paused: s.Paused, MaxImages: s.MaxImages})
	}
	return out
}

// CheckFeeds probes configured feeds only; no login, media fetching or DB IO.
func CheckFeeds(ctx context.Context, c Config, f Fetcher) error {
	failed := 0
	for i, s := range c.Feeds {
		fields := append([]any{"yaml_feed", i + 1, "channel", s.Channel}, feedFields(s.URL)...)
		v, err := fetchEntries(ctx, f, s.URL)
		if err != nil {
			failed++
			slog.Warn("feed check failed", append(fields, errorFields(err)...)...)
		} else {
			slog.Info("feed check passed", append(fields, "entries", len(v))...)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d configured feed checks failed; see sanitized diagnostics", failed, len(c.Feeds))
	}
	return nil
}

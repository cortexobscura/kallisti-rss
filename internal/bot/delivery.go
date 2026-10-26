package bot

import (
	"errors"
	"log/slog"
	"regexp"
	"strconv"
	"time"
)

// DeliveryFailure contains controlled labels only, never server text or payload.
type DeliveryFailure struct {
	Class, Scope, Stage string
	RetryAfter          time.Duration
	Rejected            bool
	errorChannel        int64
	about               string
}

func (f *DeliveryFailure) Error() string { return f.Stage + ": " + f.Class + " (" + f.Scope + ")" }
func (f *DeliveryFailure) Unwrap() error {
	if f.Rejected {
		return ErrRejected
	}
	return nil
}
func globalFailure(class, stage string) *DeliveryFailure {
	return &DeliveryFailure{Class: class, Scope: "global", Stage: stage}
}

var slowReason = regexp.MustCompile(`^Slow mode: wait ([0-9]{1,4}) s$`)

func classifyRejection(reason, about string, channel int64) *DeliveryFailure {
	f := globalFailure("server_rejected_unknown", "send")
	f.Rejected = true
	f.errorChannel = channel
	// Preserve only recognized metadata, never arbitrary about/message fields.
	switch reason {
	case "unknown channel":
		f.Class = "channel_missing"
	case "you may not use this channel's chat":
		f.Class = "channel_permission"
	case "unknown upload":
		f.Class = "attachment_rejected"
	case "too many requests, slow down":
		f.Class = "server_rate_limit"
	case "not ready":
		f.Class = "server_not_ready"
	case "cannot store message":
		f.Class = "server_storage"
	case "invalid nonce":
		f.Class = "invalid_nonce"
	case "This message links to a blocked site":
		f.Class = "content_domain_blocked"
	case "This message contains a blocked word":
		f.Class = "content_word_blocked"
	case "message must be 1 to 2000 characters", "This message is too long with the word replacements":
		f.Class = "text_length_rejected"
	}
	if about == "slow-mode" {
		if m := slowReason.FindStringSubmatch(reason); len(m) == 2 {
			n, err := strconv.Atoi(m[1])
			if err == nil && n > 0 && n <= 3600 {
				f.Class = "channel_slow_mode"
				f.RetryAfter = time.Duration(n) * time.Second
				f.about = "slow-mode"
			}
		}
	}
	return f
}

// Only called after rpc has matched the exact immutable job nonce.
func scopeRejection(err error, j Job) error {
	var f *DeliveryFailure
	if !errors.As(err, &f) || !f.Rejected {
		return err
	}
	copy := *f
	if f.errorChannel != 0 && f.errorChannel != j.Channel {
		copy.Class = "rejection_channel_mismatch"
		return &copy
	}
	switch f.Class {
	case "channel_missing", "channel_permission", "attachment_rejected":
		copy.Scope = "channel"
	case "channel_slow_mode":
		if f.errorChannel == j.Channel && f.about == "slow-mode" {
			copy.Scope = "channel"
		}
	}
	return &copy
}
func deliveryFailure(err error, stage string) *DeliveryFailure {
	var f *DeliveryFailure
	if errors.As(err, &f) {
		copy := *f
		if stage != "" {
			copy.Stage = stage
		}
		return &copy
	}
	if stage == "" {
		stage = "send"
	}
	return globalFailure("unknown_error", stage)
}
func deliveryLog(j *Job, f *DeliveryFailure, now time.Time, attempts int, next int64) {
	attrs := []any{"stage", f.Stage, "error_class", f.Class, "scope", f.Scope, "reason", deliveryReason(f.Class)}
	if j != nil {
		attrs = append(attrs, "job", j.ID, "channel", j.Channel, "first_attempt", j.First, "attachments_in_payload", len(j.Payload.Uploads))
		if j.First != 0 {
			attrs = append(attrs, "first_attempt_age_seconds", now.Unix()-j.First)
		}
	}
	if attempts > 0 {
		attrs = append(attrs, "failed_attempts", attempts, "retry_state", "durable")
	} else {
		attrs = append(attrs, "retry_state", "memory_only")
	}
	if next > 0 {
		attrs = append(attrs, "next_attempt_unix", next, "retry_in_seconds", max(int64(0), next-now.Unix()))
	}
	slog.Warn("delivery failed; retry scheduled", attrs...)
}

var ErrDeliveryExpired = errors.New("uncertain delivery blocked: dedup horizon elapsed")

type RetryInfo struct {
	Attempts int    `json:"attempts"`
	Next     int64  `json:"next_attempt"`
	Scope    string `json:"scope"`
	Class    string `json:"error_class"`
	Stage    string `json:"stage"`
}

func retryDelay(attempt int, minimum time.Duration) time.Duration {
	delay := 30 * time.Second
	for i := 1; i < attempt && delay < 15*time.Minute; i++ {
		delay *= 2
	}
	if delay > 15*time.Minute {
		delay = 15 * time.Minute
	}
	if minimum > delay {
		delay = minimum
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	return delay
}
func (e *Engine) failDelivery(j *Job, err error, stage string, now time.Time) error {
	f := deliveryFailure(err, stage)
	if f.Scope == "global" {
		e.globalUntil = now.Add(30 * time.Second)
	}
	if j == nil {
		deliveryLog(nil, f, now, 0, e.globalUntil.Unix())
		return f
	}
	retry, x := e.Store.RecordFailure(*j, f, now)
	if x != nil {
		e.globalUntil = now.Add(30 * time.Second)
		failed := globalFailure("storage_error", "retry_persist")
		deliveryLog(j, failed, now, 0, e.globalUntil.Unix())
		return failed
	}
	if f.Scope == "global" {
		e.globalUntil = time.Unix(retry.Next, 0)
	}
	deliveryLog(j, f, now, retry.Attempts, retry.Next)
	return f
}
func (e *Engine) deliveryNow() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func deliveryReason(class string) string {
	switch class {
	case "channel_permission":
		return "server denied destination channel access"
	case "channel_missing":
		return "server does not recognize destination channel"
	case "attachment_rejected":
		return "server rejected referenced attachment; attempted payload remains unchanged"
	case "channel_slow_mode":
		return "destination channel slow mode requires a delay"
	case "server_rate_limit":
		return "server request rate limit; all delivery pauses"
	case "server_storage":
		return "server could not store message"
	case "storage_error":
		return "local database operation failed"
	case "disconnected", "write_failed", "ack_timeout":
		return "connection or acknowledgment failed; delivery outcome remains uncertain"
	case "content_domain_blocked":
		return "server policy blocked a link domain"
	case "content_word_blocked":
		return "server policy blocked message content"
	case "text_length_rejected":
		return "server rejected message length"
	case "server_not_ready":
		return "server reports session not ready"
	case "invalid_nonce":
		return "server rejected request nonce"
	case "rejection_channel_mismatch":
		return "correlated rejection names a different channel"
	default:
		return "unclassified failure; raw server text omitted and global retry required"
	}
}

func imageFailureLog(j Job, stage string, err error) {
	fields := []any{"job", j.ID, "channel", j.Channel, "stage", stage}
	var d *Diagnostic
	var f *DeliveryFailure
	if errors.As(err, &d) {
		fields = append(fields, errorFields(d)...)
	} else if errors.As(err, &f) {
		fields = append(fields, "error_class", f.Class, "reason", deliveryReason(f.Class))
	} else {
		class, reason := "image_error", "image preparation failed; raw errors omitted"
		if stage == "image_decode" {
			class, reason = "image_decode", "image format, bytes or dimensions were rejected"
		}
		fields = append(fields, "error_class", class, "reason", reason)
	}
	slog.Warn("image skipped before first send; source link retained", fields...)
}

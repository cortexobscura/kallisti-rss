package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

type Sender interface {
	Send(context.Context, Job) error
	Upload(context.Context, []byte) (string, error)
}
type Engine struct {
	Config        Config
	Store         *Store
	Fetcher       Fetcher
	Sender        Sender
	Now           func() time.Time
	globalUntil   time.Time
	pollDue       map[int64]time.Time
	pollReadAfter time.Time
}

// Poll explicitly polls all subscriptions (used by fixture and one-shot callers).
func (e *Engine) Poll(ctx context.Context) error { return e.poll(ctx, false) }

// PollDue runs only due channels. Deadlines survive reconnects in this Engine;
// process restart intentionally checks all channels immediately without resetting seen state.
func (e *Engine) PollDue(ctx context.Context) error { return e.poll(ctx, true) }

func (e *Engine) poll(ctx context.Context, scheduled bool) error {
	if scheduled && e.deliveryNow().Before(e.pollReadAfter) {
		return nil
	}
	subs, err := e.Store.Subscriptions()
	if err != nil {
		e.pollReadAfter = e.deliveryNow().Add(30 * time.Second)
		return diag("storage_error", "subscription read failed; retry in 30s")
	}

	if e.pollDue == nil {
		e.pollDue = map[int64]time.Time{}
	}
	now := e.deliveryNow()
	remaining := map[string]int{}
	var selected []Subscription
	due := map[int64]bool{}
	paused := 0
	for _, sub := range subs {
		if sub.Paused {
			paused++
			continue
		}
		if scheduled && now.Before(e.pollDue[sub.Channel]) {
			continue
		}
		due[sub.Channel] = true
		remaining[sub.URL]++
		selected = append(selected, sub)
	}
	if len(due) == 0 {
		return nil
	}
	var errs []error
	passed := 0
	// Preserve subscription traversal order, with at most eight shared parsed results.
	type result struct {
		entries []Entry
		err     error
	}
	cache := map[string]result{}
	for _, s := range selected {
		r, cached := cache[s.URL]
		if !cached {
			r.entries, r.err = fetchEntries(ctx, e.Fetcher, s.URL)
			if remaining[s.URL] > 1 && len(cache) < 8 {
				cache[s.URL] = r
			}
		}
		remaining[s.URL]--
		if remaining[s.URL] == 0 {
			delete(cache, s.URL)
		}
		entries, fetchErr := r.entries, r.err
		imageEntries, candidates := 0, 0
		for _, entry := range entries {
			if len(entry.Images) > 0 {
				imageEntries++
				candidates += len(entry.Images)
			}
		}
		fields := append([]any{"subscription", s.ID, "channel", s.Channel, "yaml_feed", seedIndex(e.Config, s.Channel, s.URL)}, feedFields(s.URL)...)
		x := fetchErr
		var ingested IngestResult
		if x == nil {
			var er error
			if ingested, er = e.Store.IngestCounts(ctx, s, entries); er != nil {
				x = diag("storage_error", "ingest transaction failed; seen/outbox update not committed")
			}
		}
		if x == nil && ingested.Deferred > 0 {
			slog.Warn("feed entries deferred", append(fields, "queued", ingested.Queued, "deferred", ingested.Deferred, "max_new_per_poll", MaxNewPerPoll, "channel_pending_limit", ChannelPendingLimit, "retry", "next_channel_poll")...)
		}
		if x != nil {
			slog.Warn("feed poll failed", append(append(fields, errorFields(x)...), "baseline_pending", !s.Initialized, "retry", "next_channel_poll", "poll_interval", e.Config.ChannelPollInterval(s.Channel))...)
			errs = append(errs, fmt.Errorf("subscription %d failed: %w", s.ID, x))
		} else {
			passed++
			slog.Info("feed poll completed", append(fields, "entries", len(entries), "queued", ingested.Queued, "baseline_recorded", !s.Initialized, "image_entries", imageEntries, "image_candidates", candidates, "stage", "extraction", "poll_interval", e.Config.ChannelPollInterval(s.Channel))...)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	// Failures also wait their channel interval; no hot retry or early slower-channel ingest.
	for channel := range due {
		e.pollDue[channel] = e.deliveryNow().Add(e.Config.ChannelPollInterval(channel))
	}
	slog.Info("feed poll cycle completed", "succeeded", passed, "failed", len(errs), "paused", paused, "due_channels", len(due))
	return errors.Join(errs...)
}

func (e *Engine) Drain(ctx context.Context) error {
	var failures []error
	if e.deliveryNow().Before(e.globalUntil) {
		return nil
	}
	for n := 0; n < 100; n++ {
		now := e.deliveryNow()
		j, err := e.Store.NextReady(now)
		if err != nil {
			return e.failDelivery(nil, globalFailure("storage_error", "queue_read"), "queue_read", now)
		}
		if j == nil {
			return errors.Join(failures...)
		}

		if j.First == 0 && len(j.Payload.Images) > 0 && (j.Payload.PreparedAt == 0 || now.Unix()-j.Payload.PreparedAt >= 9*60 || now.Unix() < j.Payload.PreparedAt) {
			j.Payload.Uploads = nil
			downloaded, validated := 0, 0
			candidates := len(j.Payload.Images)
			if limiter, ok := e.Sender.(interface{ MaxImages() int }); ok {
				n := limiter.MaxImages()
				if len(j.Payload.Images) > n {
					j.Payload.Images = j.Payload.Images[:n]
				}
			}
			for _, im := range j.Payload.Images {
				imageStage := "image_fetch"
				b, er := e.Fetcher.Fetch(ctx, im, MediaLimit)
				if er == nil {
					downloaded++
					imageStage = "image_decode"
					b, er = Raster(b)
				}
				if er == nil {
					validated++
					var key string
					imageStage = "image_upload"
					key, er = e.Sender.Upload(ctx, b)
					if er == nil {
						j.Payload.Uploads = append(j.Payload.Uploads, key)
					}
				}
				if er != nil {
					imageFailureLog(*j, imageStage, er)
				}
			}
			slog.Info("image preparation completed", "job", j.ID, "channel", j.Channel, "stage", "preparation", "image_candidates", candidates, "attempted", len(j.Payload.Images), "downloaded", downloaded, "validated", validated, "uploaded", len(j.Payload.Uploads), "text_fallback", len(j.Payload.Uploads) == 0)
			j.Payload.PreparedAt = now.Unix()
			if err = e.Store.SavePayload(j); err != nil {
				return e.failDelivery(j, globalFailure("storage_error", "payload_persist"), "payload_persist", now)
			}
		}

		if err = e.Store.Prepare(j, now); err != nil {
			if errors.Is(err, ErrDeliveryExpired) {
				slog.Warn("delivery quarantined; channel awaits operator reconciliation", "job", j.ID, "channel", j.Channel, "stage", "prepare", "error_class", "dedup_horizon", "first_attempt", j.First)
				failures = append(failures, err)
				continue
			}
			return e.failDelivery(j, globalFailure("storage_error", "first_attempt_persist"), "first_attempt_persist", now)
		}
		if err = e.Sender.Send(ctx, *j); err != nil {
			failure := e.failDelivery(j, err, "", e.deliveryNow())
			if deliveryFailure(failure, "").Scope == "channel" {
				failures = append(failures, failure)
				continue
			}
			return failure
		}
		if err = e.Store.Delivered(j.ID); err != nil {
			return e.failDelivery(j, globalFailure("storage_error", "ack_commit"), "ack_commit", e.deliveryNow())
		}
		slog.Info("delivery acknowledged", "job", j.ID, "channel", j.Channel, "stage", "ack_commit", "attachments_acknowledged", len(j.Payload.Uploads), "image_candidates", len(j.Payload.Images), "text_only", len(j.Payload.Uploads) == 0)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return errors.Join(failures...)
}

type Message struct {
	ID       int64   `json:"id"`
	Channel  int64   `json:"channel"`
	AuthorID *uint32 `json:"authorId"`
	Verified bool    `json:"verified"`
	Text     string  `json:"text"`
}

func (e *Engine) Command(ctx context.Context, m Message) error {
	if !strings.HasPrefix(m.Text, "!feed ") || len(m.Text) > 2300 || m.ID <= 0 || !e.Config.Authorized(m.AuthorID, m.Verified, m.Channel) {
		return nil
	}
	a := strings.Fields(m.Text)
	if len(a) < 2 {
		return nil
	}
	tx, err := e.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.Exec("INSERT OR IGNORE INTO commands(id) VALUES(?)", m.ID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return nil
	}
	var pending int
	if err = tx.QueryRow("SELECT count(*) FROM outbox WHERE state IN ('pending','blocked')").Scan(&pending); err != nil {
		return err
	}
	if pending >= OutboxPendingLimit {
		return errors.New("outbox capacity reached")
	}
	reply := "Usage: !feed list | add HTTPS_URL | remove ID | pause ID | resume ID | help"
	switch a[1] {
	case "add":
		if len(a) == 3 {
			if _, err := ValidateURL(a[2], e.Config.AllowHTTP); err != nil {
				reply = "Feed URL rejected."
			} else {
				var n int
				err = tx.QueryRow("SELECT count(*) FROM subscriptions WHERE deleted=0").Scan(&n)
				if err != nil {
					return err
				}
				if n >= 100 {
					reply = "Subscription limit reached."
				} else {
					_, err = tx.Exec("INSERT INTO subscriptions(channel,url) VALUES(?,?) ON CONFLICT(channel,url) DO UPDATE SET deleted=0,paused=0", m.Channel, a[2])
					if err != nil {
						return err
					}
					reply = "Subscribed. New subscriptions baseline current entries; restored subscriptions retain seen history."
				}
			}
		}
	case "remove", "pause", "resume":
		if len(a) == 3 {
			id, er := strconv.ParseInt(a[2], 10, 64)
			if er == nil {
				field := "paused=1"
				if a[1] == "remove" {
					field = "deleted=1"
				}
				if a[1] == "resume" {
					field = "paused=0"
				}
				r, er := tx.Exec("UPDATE subscriptions SET "+field+" WHERE id=? AND channel=? AND deleted=0", id, m.Channel)
				if er != nil {
					return er
				}
				n, _ := r.RowsAffected()
				reply = "No matching subscription in this channel."
				if n > 0 {
					reply = "Subscription updated. Already queued posts remain in the outbox."
				}
			}
		}
	case "list":
		rows, er := tx.Query("SELECT id,url,paused FROM subscriptions WHERE channel=? AND deleted=0 ORDER BY id", m.Channel)
		if er != nil {
			return er
		}
		reply = "Subscriptions:"
		for rows.Next() {
			var id int64
			var u string
			var p bool
			if er = rows.Scan(&id, &u, &p); er != nil {
				_ = rows.Close()
				return er
			}
			reply += fmt.Sprintf("\n%d paused=%t %s", id, p, u)
		}
		er = rows.Err()
		_ = rows.Close()
		if er != nil {
			return er
		}
	}
	if err = enqueue(tx, m.Channel, Payload{Text: cut(reply, 2000)}); err != nil {
		return err
	}
	return tx.Commit()
}

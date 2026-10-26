package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func queueJobs(t *testing.T, s *Store, channels ...int64) []Job {
	t.Helper()
	tx, e := s.DB.Begin()
	must(t, e)
	for i, ch := range channels {
		must(t, enqueue(tx, ch, Payload{Text: fmt.Sprintf("original synthetic fixture %d", i)}))
	}
	must(t, tx.Commit())
	jobs, e := s.Inspect()
	must(t, e)
	return jobs
}
func wireSender(t *testing.T, f *Fixture) *Connection {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.Handler))
	t.Cleanup(srv.Close)
	c, e := dialConnection(context.Background(), Config{Server: srv.URL, Origin: srv.URL, Username: "fixture"}, "fixture-only-token", srv.Client())
	must(t, e)
	t.Cleanup(c.Close)
	return c
}
func TestChannelIsolationWireAndBarrier(t *testing.T) {
	logs := captureLogs(t)
	s := testStore(t)
	jobs := queueJobs(t, s, 5, 7, 5, 6)
	f := &Fixture{Posts: map[string]Job{}, RejectChannels: map[int64]string{5: "you may not use this channel's chat"}}
	c := wireSender(t, f)
	now := time.Unix(2000000000, 0)
	eng := Engine{Store: s, Sender: c, Now: func() time.Time { return now }}
	if eng.Drain(context.Background()) == nil {
		t.Fatal("local failure not reported")
	}
	f.mu.Lock()
	if !reflect.DeepEqual(f.RequestChannels, []int64{5, 7, 6}) {
		t.Fatal("cross-channel progress or ordering", f.RequestChannels)
	}
	f.mu.Unlock()
	pending, e := s.Inspect()
	must(t, e)
	if len(pending) != 2 || pending[0].ID != jobs[0].ID || pending[1].ID != jobs[2].ID || pending[1].First != 0 || pending[0].Retry.Scope != "channel" {
		t.Fatal("queue corrupted", pending)
	}
	must(t, eng.Drain(context.Background()))
	f.mu.Lock()
	if len(f.RequestChannels) != 3 {
		t.Fatal("hot retry")
	}
	f.mu.Unlock()
	if strings.Count(logs.String(), "delivery failed; retry scheduled") != 1 {
		t.Fatal("warning spam", logs.String())
	}
	now = now.Add(31 * time.Second)
	if eng.Drain(context.Background()) == nil {
		t.Fatal("retry expected")
	}
	after, e := s.Inspect()
	must(t, e)
	if after[0].Nonce != jobs[0].Nonce || !reflect.DeepEqual(after[0].Payload, jobs[0].Payload) || after[0].First != pending[0].First || after[0].Retry.Attempts != 2 || after[0].Retry.Next != now.Unix()+60 {
		t.Fatal("retry identity/backoff changed", after[0])
	}
}
func TestGlobalFailuresStopOtherChannels(t *testing.T) {
	for _, reason := range []string{"too many requests, slow down", "cannot store message", "not ready", "unknown secret=https://example.com/?token=private"} {
		t.Run(reason[:min(len(reason), 10)], func(t *testing.T) {
			logs := captureLogs(t)
			s := testStore(t)
			queueJobs(t, s, 5, 7, 6)
			f := &Fixture{Posts: map[string]Job{}, RejectChannels: map[int64]string{5: reason}}
			c := wireSender(t, f)
			eng := Engine{Store: s, Sender: c}
			if eng.Drain(context.Background()) == nil {
				t.Fatal("global failure lost")
			}
			f.mu.Lock()
			if !reflect.DeepEqual(f.RequestChannels, []int64{5}) {
				t.Fatal("global rejection advanced other channels", f.RequestChannels)
			}
			f.mu.Unlock()
			if strings.Contains(logs.String(), "private") || strings.Contains(logs.String(), "https://") {
				t.Fatal("untrusted error leaked")
			}
			must(t, eng.Drain(context.Background()))
		})
	}
}
func TestDisconnectGlobalAndDurableAckLossRetry(t *testing.T) {
	captureLogs(t)
	path := filepath.Join(t.TempDir(), "db")
	s, e := OpenStore(path)
	must(t, e)
	jobs := queueJobs(t, s, 5, 7)
	f := &Fixture{Posts: map[string]Job{}, LoseAck: true}
	c := wireSender(t, f)
	now := time.Unix(2000000000, 0)
	eng := Engine{Store: s, Sender: c, Now: func() time.Time { return now }}
	if eng.Drain(context.Background()) == nil {
		t.Fatal("lost ack accepted")
	}
	snapshot, e := s.Inspect()
	must(t, e)
	if snapshot[1].First != 0 || snapshot[0].Retry.Scope != "global" {
		t.Fatal("disconnect advanced queue")
	}
	must(t, s.Close())
	s, e = OpenStore(path)
	must(t, e)
	defer s.Close()
	c.Close()
	c = wireSender(t, f)
	eng = Engine{Store: s, Sender: c, Now: func() time.Time { return now }}
	must(t, eng.Drain(context.Background()))
	f.mu.Lock()
	if len(f.RequestChannels) != 1 {
		t.Fatal("restart lost global cooldown")
	}
	f.mu.Unlock()
	now = now.Add(31 * time.Second)
	must(t, eng.Drain(context.Background()))
	f.mu.Lock()
	if len(f.Posts) != 2 || len(f.RequestChannels) != 3 {
		t.Fatal("ack-loss retry duplicated or stalled")
	}
	if f.Posts[jobs[0].Nonce].Channel != 5 {
		t.Fatal("job rerouted")
	}
	f.mu.Unlock()
	status, e := s.Status()
	must(t, e)
	if status != "pending=0 blocked=0 delivered=2" {
		t.Fatal(status)
	}
}
func TestRetryMigrationAndLocalRestart(t *testing.T) {
	captureLogs(t)
	path := filepath.Join(t.TempDir(), "db")
	s, e := OpenStore(path)
	must(t, e)
	jobs := queueJobs(t, s, 5, 7, 5)
	_, e = s.DB.Exec("DROP TABLE delivery_retry")
	must(t, e)
	must(t, s.Close())
	s, e = OpenStore(path)
	must(t, e)
	now := time.Unix(2000000000, 0)
	j, e := s.Next()
	must(t, e)
	must(t, s.Prepare(j, now))
	_, e = s.RecordFailure(*j, &DeliveryFailure{Class: "channel_permission", Scope: "channel", Stage: "send"}, now)
	must(t, e)
	must(t, s.Close())
	s, e = OpenStore(path)
	must(t, e)
	defer s.Close()
	next, e := s.NextReady(now)
	must(t, e)
	if next.ID != jobs[1].ID {
		t.Fatal("local cooldown blocked other channel")
	}
	all, e := s.Inspect()
	must(t, e)
	if all[0].Nonce != jobs[0].Nonce || all[0].Retry.Attempts != 1 || all[2].First != 0 {
		t.Fatal("migration/restart changed jobs")
	}
	must(t, s.Delivered(next.ID))
	next, e = s.NextReady(now)
	must(t, e)
	if next != nil {
		t.Fatal("same channel bypassed barrier")
	}
}
func TestQuarantineKeepsSameChannelBarrier(t *testing.T) {
	captureLogs(t)
	s := testStore(t)
	jobs := queueJobs(t, s, 5, 5, 7)
	j, e := s.Next()
	must(t, e)
	must(t, s.Prepare(j, time.Unix(1000, 0)))
	sender := &recordSender{}
	eng := Engine{Store: s, Sender: sender, Now: func() time.Time { return time.Unix(1000+23*3600, 0) }}
	if eng.Drain(context.Background()) == nil {
		t.Fatal("expired job not reported")
	}
	if len(sender.sent) != 1 || sender.sent[0].ID != jobs[2].ID {
		t.Fatal("quarantine ordering violated")
	}
	all, e := s.Inspect()
	must(t, e)
	if all[0].State != "blocked" || all[1].First != 0 {
		t.Fatal("blocked job advanced channel")
	}
}

type callbackSender struct{ send func(Job) error }

func (s callbackSender) Send(_ context.Context, j Job) error { return s.send(j) }
func (s callbackSender) Upload(context.Context, []byte) (string, error) {
	return "", errors.New("not used")
}
func TestStorageAckCommitFailureStopsGlobally(t *testing.T) {
	logs := captureLogs(t)
	s := testStore(t)
	queueJobs(t, s, 5, 7)
	calls := 0
	sender := callbackSender{send: func(j Job) error {
		calls++
		_, e := s.DB.Exec(`CREATE TRIGGER fail_delivered BEFORE UPDATE OF state ON outbox WHEN NEW.state='delivered' BEGIN SELECT RAISE(FAIL,'private db details'); END`)
		return e
	}}
	eng := Engine{Store: s, Sender: sender}
	if eng.Drain(context.Background()) == nil {
		t.Fatal("DB failure lost")
	}
	if calls != 1 {
		t.Fatal("DB failure did not stop")
	}
	all, e := s.Inspect()
	must(t, e)
	if all[0].State != "pending" || all[0].Retry.Stage != "ack_commit" || all[1].First != 0 {
		t.Fatal("DB failure corrupted queue")
	}
	if strings.Contains(logs.String(), "private db") {
		t.Fatal("DB error leaked")
	}
	must(t, eng.Drain(context.Background()))
	if calls != 1 {
		t.Fatal("hot DB retry")
	}
}
func TestStorageFailureFallbackCooldown(t *testing.T) {
	logs := captureLogs(t)
	s := testStore(t)
	queueJobs(t, s, 5, 7)
	_, e := s.DB.Exec(`CREATE TRIGGER fail_retry BEFORE INSERT ON delivery_retry BEGIN SELECT RAISE(FAIL,'secret'); END`)
	must(t, e)
	calls := 0
	eng := Engine{Store: s, Sender: callbackSender{send: func(Job) error { calls++; return globalFailure("disconnected", "acknowledgment") }}}
	if eng.Drain(context.Background()) == nil {
		t.Fatal("retry persistence failure lost")
	}
	must(t, eng.Drain(context.Background()))
	if calls != 1 || strings.Count(logs.String(), "delivery failed;") != 1 || strings.Contains(logs.String(), "secret") {
		t.Fatal("fallback cooldown/redaction failed")
	}
}
func TestRejectionClassificationAndCorrelation(t *testing.T) {
	cases := []struct {
		reason, about string
		channel       int64
		scope, class  string
	}{{"unknown channel", "", 0, "channel", "channel_missing"}, {"you may not use this channel's chat", "", 5, "channel", "channel_permission"}, {"unknown upload", "", 5, "channel", "attachment_rejected"}, {"Slow mode: wait 60 s", "slow-mode", 5, "channel", "channel_slow_mode"}, {"Slow mode: wait 60 s", "", 5, "global", "server_rejected_unknown"}, {"Slow mode: wait 60 s", "slow-mode", 7, "global", "rejection_channel_mismatch"}, {"too many requests, slow down", "refused", 5, "global", "server_rate_limit"}, {"unknown user token=secret", "", 5, "global", "server_rejected_unknown"}, {strings.Repeat("secret", 1000), "", 5, "global", "server_rejected_unknown"}}
	for _, v := range cases {
		b, e := json.Marshal(map[string]any{"type": "error", "message": v.reason, "nonce": "n", "channel": v.channel, "about": v.about})
		must(t, e)
		var ev Event
		must(t, json.Unmarshal(b, &ev))
		f := deliveryFailure(scopeRejection(ev.rejection, Job{Channel: 5}), "")
		if f.Scope != v.scope || f.Class != v.class || strings.Contains(f.Error(), "secret") {
			t.Fatalf("unsafe classification %+v", f)
		}
	}
}
func TestWrongNonceCannotIsolateChannel(t *testing.T) {
	captureLogs(t)
	f := &Fixture{Posts: map[string]Job{}, RejectChannels: map[int64]string{5: "unknown channel"}, WrongNonce: true}
	c := wireSender(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := c.Send(ctx, Job{Channel: 5, Nonce: "expected", Payload: Payload{Text: "fixture"}})
	failure := deliveryFailure(err, "")
	if failure.Scope != "global" || failure.Class != "ack_timeout" {
		t.Fatal("uncorrelated error treated as local", failure)
	}
}
func TestBackoffBounds(t *testing.T) {
	if retryDelay(1, 0) != 30*time.Second || retryDelay(100, 0) != 15*time.Minute || retryDelay(1, time.Hour) != time.Hour {
		t.Fatal("backoff bounds")
	}
}

func TestMediaDiagnosticRedaction(t *testing.T) {
	logs := captureLogs(t)
	imageFailureLog(Job{ID: 15, Channel: 5}, "image_upload", errors.New("https://user:password@host/private?token=secret"))
	text := logs.String()
	if strings.Contains(text, "secret") || strings.Contains(text, "password") || !strings.Contains(text, `"channel":5`) || !strings.Contains(text, `"stage":"image_upload"`) {
		t.Fatal("media diagnostics unsafe or incomplete")
	}
}

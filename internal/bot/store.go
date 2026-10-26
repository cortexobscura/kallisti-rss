package bot

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
	"os"
	"time"
)

type Store struct {
	DB   *sql.DB
	lock *os.File
}
type Subscription struct {
	ID, Channel                  int64
	URL                          string
	Initialized, Paused, Deleted bool
	MaxImages                    int
}
type Payload struct {
	Text       string   `json:"text"`
	Images     []string `json:"images,omitempty"`
	Uploads    []string `json:"uploads,omitempty"`
	PreparedAt int64    `json:"prepared_at,omitempty"`
}
type Job struct {
	ID, Channel int64
	Nonce       string
	Payload     Payload
	First       int64
	State       string
	Retry       *RetryInfo `json:"Retry,omitempty"`
}

func OpenStore(path string) (*Store, error) {
	// #nosec G304 -- operator-selected local database path; chat/feed data never selects filesystem paths.
	f, e := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		_ = f.Close()
		return nil, errors.New("database already in use")
	}
	// #nosec G304 -- operator-selected local database path, not remote input.
	file, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		_ = f.Close()
		return nil, e
	}
	_ = file.Close()
	db, e := sql.Open("sqlite", path)
	if e != nil {
		_ = f.Close()
		return nil, e
	}
	db.SetMaxOpenConns(1)
	s := &Store{db, f}
	_, e = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY,v TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS subscriptions(id INTEGER PRIMARY KEY,channel INTEGER NOT NULL,url TEXT NOT NULL,initialized INTEGER NOT NULL DEFAULT 0,paused INTEGER NOT NULL DEFAULT 0,deleted INTEGER NOT NULL DEFAULT 0,max_images INTEGER NOT NULL DEFAULT 3,UNIQUE(channel,url));
 CREATE TABLE IF NOT EXISTS seen(sub INTEGER NOT NULL,item TEXT NOT NULL,PRIMARY KEY(sub,item));
 CREATE TABLE IF NOT EXISTS outbox(id INTEGER PRIMARY KEY,channel INTEGER NOT NULL,nonce TEXT UNIQUE NOT NULL,payload TEXT NOT NULL,first INTEGER NOT NULL DEFAULT 0,state TEXT NOT NULL DEFAULT 'pending');
 CREATE TABLE IF NOT EXISTS commands(id INTEGER PRIMARY KEY);
 CREATE TABLE IF NOT EXISTS delivery_retry(job INTEGER PRIMARY KEY,attempts INTEGER NOT NULL,next_at INTEGER NOT NULL,scope TEXT NOT NULL,class TEXT NOT NULL,stage TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS outbox_channel_order ON outbox(channel,id,state);`)
	if e != nil {
		_ = s.Close()
		return nil, e
	}
	return s, nil
}
func (s *Store) Close() error {
	e := s.DB.Close()
	if s.lock != nil {
		_ = unix.Flock(int(s.lock.Fd()), unix.LOCK_UN)
		_ = s.lock.Close()
	}
	return e
}
func (s *Store) Bind(identity string) error {
	_, e := s.DB.Exec("INSERT OR IGNORE INTO meta(k,v) VALUES('identity',?)", identity)
	if e != nil {
		return e
	}
	var got string
	e = s.DB.QueryRow("SELECT v FROM meta WHERE k='identity'").Scan(&got)
	if e != nil {
		return e
	}
	if got != identity {
		return errors.New("database belongs to another server/account; use original identity")
	}
	return nil
}
func (s *Store) Seed(seeds []Seed) error {
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var count int
	if e = tx.QueryRow("SELECT count(*) FROM subscriptions WHERE deleted=0").Scan(&count); e != nil {
		return e
	}
	for _, v := range seeds {
		var exists int
		e = tx.QueryRow("SELECT 1 FROM subscriptions WHERE channel=? AND url=?", v.Channel, v.URL).Scan(&exists)
		if e == nil {
			continue
		}
		if e != sql.ErrNoRows {
			return e
		}
		if count >= 100 {
			return errors.New("active subscription limit reached")
		}
		if _, e = tx.Exec("INSERT INTO subscriptions(channel,url,max_images) VALUES(?,?,?)", v.Channel, v.URL, imageCount(v.MaxImages)); e != nil {
			return e
		}
		count++
	}
	return tx.Commit()
}

func (s *Store) Subscriptions() ([]Subscription, error) {
	rows, e := s.DB.Query("SELECT id,channel,url,initialized,paused,deleted,max_images FROM subscriptions WHERE deleted=0 ORDER BY id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var v []Subscription
	for rows.Next() {
		var x Subscription
		if e = rows.Scan(&x.ID, &x.Channel, &x.URL, &x.Initialized, &x.Paused, &x.Deleted, &x.MaxImages); e != nil {
			return nil, e
		}
		v = append(v, x)
	}
	return v, rows.Err()
}
func nonce() string {
	var b [24]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func enqueue(tx *sql.Tx, ch int64, p Payload) error {
	b, e := json.Marshal(p)
	if e != nil {
		return e
	}
	_, e = tx.Exec("INSERT INTO outbox(channel,nonce,payload) VALUES(?,?,?)", ch, nonce(), string(b))
	return e
}
func (s *Store) Ingest(ctx context.Context, sub Subscription, entries []Entry) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var initialized, paused, deleted bool
	if e = tx.QueryRow("SELECT initialized,paused,deleted FROM subscriptions WHERE id=?", sub.ID).Scan(&initialized, &paused, &deleted); e != nil {
		return e
	}
	if paused || deleted {
		return nil
	}
	var pending int
	if e = tx.QueryRow("SELECT count(*) FROM outbox WHERE state IN ('pending','blocked')").Scan(&pending); e != nil {
		return e
	}
	for i := len(entries) - 1; i >= 0; i-- {
		x := entries[i]
		r, e := tx.Exec("INSERT OR IGNORE INTO seen(sub,item) VALUES(?,?)", sub.ID, x.ID)
		if e != nil {
			return e
		}
		n, _ := r.RowsAffected()
		if n > 0 && initialized {
			if pending >= 10000 {
				return errors.New("outbox capacity reached; ingest rolled back")
			}
			if e = enqueue(tx, sub.Channel, Payload{Text: Text(x), Images: entryImages(x, sub.MaxImages)}); e != nil {
				return e
			}
			pending++
		}
	}
	if _, e = tx.Exec("UPDATE subscriptions SET initialized=1 WHERE id=?", sub.ID); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Next() (*Job, error) {
	var j Job
	var p string
	e := s.DB.QueryRow("SELECT id,channel,nonce,payload,first,state FROM outbox WHERE state='pending' ORDER BY id LIMIT 1").Scan(&j.ID, &j.Channel, &j.Nonce, &p, &j.First, &j.State)
	if e == sql.ErrNoRows {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	e = json.Unmarshal([]byte(p), &j.Payload)
	return &j, e
}
func (s *Store) Prepare(j *Job, now time.Time) error {
	if j.First != 0 && (now.Unix() < j.First || now.Unix()-j.First >= int64((23*time.Hour)/time.Second)) {
		_, e := s.DB.Exec("UPDATE outbox SET state='blocked' WHERE id=?", j.ID)
		if e != nil {
			return e
		}
		return ErrDeliveryExpired
	}
	if j.First == 0 {
		j.First = now.Unix()
		_, e := s.DB.Exec("UPDATE outbox SET first=? WHERE id=?", j.First, j.ID)
		return e
	}
	return nil
}
func (s *Store) SavePayload(j *Job) error {
	b, e := json.Marshal(j.Payload)
	if e != nil {
		return e
	}
	_, e = s.DB.Exec("UPDATE outbox SET payload=? WHERE id=? AND first=0", string(b), j.ID)
	return e
}
func (s *Store) Delivered(id int64) error {
	_, e := s.DB.Exec("UPDATE outbox SET state='delivered' WHERE id=?", id)
	return e
}
func (s *Store) Status() (string, error) {
	var p, b, d int
	e := s.DB.QueryRow("SELECT coalesce(sum(state='pending'),0),coalesce(sum(state='blocked'),0),coalesce(sum(state='delivered'),0) FROM outbox").Scan(&p, &b, &d)
	return fmt.Sprintf("pending=%d blocked=%d delivered=%d", p, b, d), e
}

func imageCount(n int) int {
	if n == 0 {
		return 3
	}
	return n
}
func entryImages(e Entry, n int) []string {
	im := e.Images
	if len(im) == 0 && e.Image != "" {
		im = []string{e.Image}
	}
	n = imageCount(n)
	if len(im) > n {
		im = im[:n]
	}
	return im
}

func (s *Store) BindUser(id uint32) error {
	want := fmt.Sprint(id)
	_, e := s.DB.Exec("INSERT OR IGNORE INTO meta(k,v) VALUES('registered_user',?)", want)
	if e != nil {
		return e
	}
	var got string
	e = s.DB.QueryRow("SELECT v FROM meta WHERE k='registered_user'").Scan(&got)
	if e != nil {
		return e
	}
	if got != want {
		return errors.New("registered bot identity changed; refusing old outbox")
	}
	return nil
}

// Inspect is an offline operator view; feed-controlled payloads are JSON encoded.
func (s *Store) Inspect() ([]Job, error) {
	rows, e := s.DB.Query("SELECT o.id,o.channel,o.nonce,o.payload,o.first,o.state,coalesce(r.attempts,0),coalesce(r.next_at,0),coalesce(r.scope,''),coalesce(r.class,''),coalesce(r.stage,'') FROM outbox o LEFT JOIN delivery_retry r ON r.job=o.id WHERE o.state IN ('pending','blocked') ORDER BY o.id LIMIT 1000")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		var p string
		var retry RetryInfo
		if e = rows.Scan(&j.ID, &j.Channel, &j.Nonce, &p, &j.First, &j.State, &retry.Attempts, &retry.Next, &retry.Scope, &retry.Class, &retry.Stage); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(p), &j.Payload); e != nil {
			return nil, e
		}
		if retry.Attempts > 0 {
			j.Retry = &retry
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
func (s *Store) Resolve(id int64, state string) error {
	if state != "delivered" && state != "discarded" {
		return errors.New("resolution must be delivered or discarded")
	}
	r, e := s.DB.Exec("UPDATE outbox SET state=? WHERE id=? AND state IN ('pending','blocked')", state, id)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return errors.New("no unresolved job with that ID")
	}
	return nil
}

// NextReady keeps a barrier per channel, including quarantined uncertain jobs.
// A persisted global cooldown prevents progress across every channel.
func (s *Store) NextReady(now time.Time) (*Job, error) {
	var j Job
	var p string
	e := s.DB.QueryRow(`SELECT o.id,o.channel,o.nonce,o.payload,o.first,o.state FROM outbox o LEFT JOIN delivery_retry r ON r.job=o.id
 WHERE o.state='pending' AND coalesce(r.next_at,0)<=?
 AND NOT EXISTS(SELECT 1 FROM outbox older WHERE older.channel=o.channel AND older.id<o.id AND older.state IN ('pending','blocked'))
 AND NOT EXISTS(SELECT 1 FROM delivery_retry g JOIN outbox q ON q.id=g.job WHERE q.state='pending' AND g.scope='global' AND g.next_at>?)
 ORDER BY o.id LIMIT 1`, now.Unix(), now.Unix()).Scan(&j.ID, &j.Channel, &j.Nonce, &p, &j.First, &j.State)
	if e == sql.ErrNoRows {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	e = json.Unmarshal([]byte(p), &j.Payload)
	return &j, e
}
func (s *Store) RecordFailure(j Job, f *DeliveryFailure, now time.Time) (RetryInfo, error) {
	var r RetryInfo
	tx, e := s.DB.Begin()
	if e != nil {
		return r, e
	}
	defer tx.Rollback()
	e = tx.QueryRow("SELECT attempts FROM delivery_retry WHERE job=?", j.ID).Scan(&r.Attempts)
	if e != nil && e != sql.ErrNoRows {
		return r, e
	}
	if r.Attempts < 1000000 {
		r.Attempts++
	}
	r.Next = now.Add(retryDelay(r.Attempts, f.RetryAfter)).Unix()
	if j.First > 0 {
		r.Next = min(r.Next, j.First+23*3600)
	}
	r.Scope, r.Class, r.Stage = f.Scope, f.Class, f.Stage
	_, e = tx.Exec(`INSERT INTO delivery_retry(job,attempts,next_at,scope,class,stage) VALUES(?,?,?,?,?,?) ON CONFLICT(job) DO UPDATE SET attempts=excluded.attempts,next_at=excluded.next_at,scope=excluded.scope,class=excluded.class,stage=excluded.stage`, j.ID, r.Attempts, r.Next, r.Scope, r.Class, r.Stage)
	if e != nil {
		return r, e
	}
	return r, tx.Commit()
}

// PauseSubscription is an offline operator action guarded by the stored destination.
// The caller holds OpenStore's exclusive lock. No seen or outbox rows are changed.
func (s *Store) PauseSubscription(id, expectedChannel int64) error {
	if id <= 0 || expectedChannel <= 0 {
		return errors.New("subscription and expected channel must be positive")
	}
	r, err := s.DB.Exec("UPDATE subscriptions SET paused=1 WHERE id=? AND channel=? AND deleted=0", id, expectedChannel)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("no active subscription with that ID and expected channel; nothing paused")
	}
	return nil
}

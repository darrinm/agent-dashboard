package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var ErrStaleEpisode = errors.New("request is no longer current")
var ErrInvalidAcknowledgment = errors.New("unsupported acknowledgment for this request")

type Store struct {
	DB                 *sql.DB
	mu                 sync.Mutex
	sessions           map[string]Session
	health             map[string]Health
	listeners          map[chan struct{}]bool
	seq                int64
	MachineID, Machine string
	DataDir            string
}

func OpenStore(dir, machine string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "agents.sqlite"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS metadata (key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS sessions (id TEXT PRIMARY KEY, body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS events (seq INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, session_id TEXT NOT NULL, body BLOB NOT NULL, sent INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS tail_state (path TEXT PRIMARY KEY, body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS devices (id TEXT PRIMARY KEY, token_hash TEXT UNIQUE NOT NULL, name TEXT NOT NULL, last_seen INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS enrollments (token_hash TEXT PRIMARY KEY, expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS receipts (device TEXT NOT NULL, event_id TEXT NOT NULL, PRIMARY KEY(device,event_id));
CREATE TABLE IF NOT EXISTS watermarks (device TEXT NOT NULL, session_id TEXT NOT NULL, seq INTEGER NOT NULL, PRIMARY KEY(device,session_id));
CREATE TABLE IF NOT EXISTS tombstones (session_id TEXT PRIMARY KEY, device TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS events_pending ON events(sent,seq);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	_ = os.Chmod(filepath.Join(dir, "agents.sqlite"), 0600)
	s := &Store{DB: db, sessions: map[string]Session{}, health: map[string]Health{}, listeners: map[chan struct{}]bool{}, Machine: machine, DataDir: dir}
	if err = db.QueryRow("SELECT value FROM metadata WHERE key='machine_id'").Scan(&s.MachineID); errors.Is(err, sql.ErrNoRows) {
		s.MachineID = ID()
		_, err = db.Exec("INSERT INTO metadata(key,value) VALUES('machine_id',?)", s.MachineID)
	}
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT body FROM sessions")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b []byte
		var v Session
		if rows.Scan(&b) == nil && json.Unmarshal(b, &v) == nil {
			s.sessions[v.ID] = v
		}
	}
	rows.Close()
	_ = db.QueryRow("SELECT COALESCE(MAX(seq),0) FROM events").Scan(&s.seq)
	return s, nil
}
func (s *Store) wake() {
	for ch := range s.listeners {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
func (s *Store) Subscribe() (chan struct{}, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := make(chan struct{}, 1)
	s.listeners[c] = true
	return c, func() { s.mu.Lock(); delete(s.listeners, c); s.mu.Unlock() }
}
func (s *Store) Health(h Health) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.health[h.ID]
	h.UpdatedAt = time.Now().UTC()
	s.health[h.ID] = h
	if old.Status != h.Status || old.Detail != h.Detail {
		s.wake()
	}
}
// ClearHealth removes a health entry that hasn't been reported again for at least
// minAge, so a passed condition stays visible for a while before disappearing.
func (s *Store) ClearHealth(id string, minAge time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.health[id]; ok && time.Since(h.UpdatedAt) >= minAge {
		delete(s.health, id)
		s.wake()
	}
}
// Sessions returns unsorted copies of the stored sessions that match.
func (s *Store) Sessions(match func(Session) bool) []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Session
	for _, v := range s.sessions {
		if match(v) {
			out = append(out, v)
		}
	}
	return out
}
func (s *Store) Get(id string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.sessions[id]
	return v, ok
}
func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := Snapshot{Version: Version, Sequence: s.seq, MachineID: s.MachineID, Machine: s.Machine, Sessions: []Session{}, Sources: []Health{}}
	for _, v := range s.sessions {
		if !v.ObservedAt.IsZero() {
			age := time.Since(v.ObservedAt)
			if age > 2*time.Minute {
				v.Connectivity = "offline"
			} else if age > 45*time.Second {
				v.Connectivity = "stale"
			}
		}
		o.Sessions = append(o.Sessions, v)
	}
	for _, h := range s.health {
		o.Sources = append(o.Sources, h)
	}
	SortSessions(o.Sessions)
	return o
}
func (s *Store) Apply(v Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists := s.sessions[v.ID]
	if exists && v.ObservedAt.Before(old.ObservedAt) {
		return nil
	}
	// A write that isn't a newer hook cannot erase a pending hook request without
	// newer evidence: turn activity, or a source vouching the session isn't waiting,
	// after the hook. Other hook-derived state is not pinned.
	if !v.Remote && !v.HookAt.After(old.HookAt) && HookRequestPending(old) &&
		v.LastActivity.Before(old.HookAt) && !v.notWaitingAt.After(old.HookAt) {
		v.RequestKind, v.RequestKey = old.RequestKind, old.RequestKey
		v.Execution, v.Outcome = old.Execution, old.Outcome
		v.HookAt, v.StateSince, v.Summary = old.HookAt, old.StateSince, old.Summary
		v.Evidence = "provider event"
	}
	if !v.Remote {
		if v.RequestKind != "" {
			key := Hash(v.ID + ":" + v.RequestKind + ":" + v.RequestKey)
			if old.Attention != nil && old.Attention.ID == key {
				e := *old.Attention
				v.Attention = &e
			} else {
				v.Attention = &Episode{ID: key, Kind: v.RequestKind, OpenedAt: v.StateSince}
			}
		} else {
			v.Attention = nil
		}
	} else if old.Attention != nil && v.Attention != nil && old.Attention.ID == v.Attention.ID {
		e := *v.Attention
		e.Seen = old.Attention.Seen
		e.Notified = old.Attention.Notified
		e.SnoozedUntil = old.Attention.SnoozedUntil
		v.Attention = &e
	}
	if v.Aliases == nil {
		v.Aliases = []string{}
	}
	if v.Buckets == nil {
		v.Buckets = []Bucket{}
	}
	if v.Capabilities == nil {
		v.Capabilities = []string{}
	}
	compareOld, compareNew := old, v
	compareOld.ObservedAt = time.Time{}
	compareNew.ObservedAt = time.Time{}
	if exists && string(wireJSON(compareOld)) == string(wireJSON(compareNew)) {
		s.sessions[v.ID] = v
		return nil
	}
	b := wireJSON(v)
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO sessions VALUES(?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body", v.ID, b); err != nil {
		return err
	}
	seq := s.seq + 1
	// Only a paired collector keeps an outbox. Pairing exports the current
	// projection, so events recorded before pairing would never be needed.
	if v.Remote || s.remoteConfigured() {
		sent := 0
		if v.Remote {
			sent = 1
		}
		r, err := tx.Exec("INSERT INTO events(at,session_id,body,sent) VALUES(?,?,?,?)", time.Now().Unix(), v.ID, b, sent)
		if err != nil {
			return err
		}
		id, _ := r.LastInsertId()
		seq = max(seq, id)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.seq = seq
	s.sessions[v.ID] = v
	s.wake()
	return nil
}
func (s *Store) Acknowledge(id, episode, action string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.sessions[id]
	if !ok || v.Attention == nil || v.Attention.ID != episode {
		return ErrStaleEpisode
	}
	e := *v.Attention
	switch action {
	case "seen":
		e.Seen = true
	case "notified":
		e.Notified = true
	case "snooze":
		e.SnoozedUntil = time.Now().Add(time.Hour)
	case "reviewed":
		if e.Kind != "review" {
			return ErrInvalidAcknowledgment
		}
		e.Seen = true
	default:
		return ErrInvalidAcknowledgment
	}
	v.Attention = &e
	if _, err := s.DB.Exec("UPDATE sessions SET body=? WHERE id=?", wireJSON(v), id); err != nil {
		return err
	}
	s.sessions[id] = v
	s.wake()
	return nil
}
func (s *Store) LoadTail(path string, into any) bool {
	var b []byte
	return s.DB.QueryRow("SELECT body FROM tail_state WHERE path=?", path).Scan(&b) == nil && json.Unmarshal(b, into) == nil
}
func (s *Store) SaveTail(path string, v any) {
	_, _ = s.DB.Exec("INSERT INTO tail_state VALUES(?,?) ON CONFLICT(path) DO UPDATE SET body=excluded.body", path, wireJSON(v))
}
func (s *Store) remoteConfigured() bool {
	_, err := os.Stat(filepath.Join(s.DataDir, "remote.json"))
	return err == nil
}
func (s *Store) Prune() error {
	configured := s.remoteConfigured()
	if !configured {
		// An unpaired collector has no use for queued events.
		r, err := s.DB.Exec("DELETE FROM events WHERE sent=0")
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n > 1000 {
			_, _ = s.DB.Exec("VACUUM")
		}
	}
	// Keep the pending queue bounded. Dropped entries are surfaced, never silently replayed.
	var size int64
	_ = s.DB.QueryRow("SELECT COALESCE(SUM(length(body)),0) FROM events WHERE sent=0").Scan(&size)
	gap := false
	if size > 100*1024*1024 {
		gap = true
		_, err := s.DB.Exec(`DELETE FROM events WHERE seq IN (
SELECT seq FROM (SELECT seq,SUM(length(body)) OVER (ORDER BY seq DESC) AS cumulative FROM events WHERE sent=0) WHERE cumulative>?)`, 100*1024*1024)
		if configured {
			s.Health(Health{ID: "outbox", Name: "Remote delivery", Status: "degraded", Detail: "History gap: local outbox reached its size limit"})
		}
		if err != nil {
			return err
		}
	}
	var pending int
	_ = s.DB.QueryRow("SELECT count(*) FROM events WHERE sent=0 AND at<?", time.Now().Add(-7*24*time.Hour).Unix()).Scan(&pending)
	if pending > 0 && configured {
		gap = true
		s.Health(Health{ID: "outbox", Name: "Remote delivery", Status: "degraded", Detail: "History gap: unsent events exceeded seven-day retention"})
	}
	if !gap {
		s.ClearHealth("outbox", time.Hour)
	}
	_, err := s.DB.Exec("DELETE FROM events WHERE at<?", time.Now().Add(-7*24*time.Hour).Unix())
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for id, v := range s.sessions {
		if expiredSummary(v) {
			if _, err = s.DB.Exec("DELETE FROM sessions WHERE id=?", id); err != nil {
				return err
			}
			if v.Transcript != "" {
				// Keep the byte cursor so expired history is not re-imported on
				// every scan; remove retained text and detailed activity.
				var t Tail
				if s.LoadTail(v.Transcript, &t) {
					t.Summary = ""
					t.Branch = ""
					t.Alias = ""
					t.Usage = nil
					t.Tools = nil
					s.SaveTail(v.Transcript, &t)
				}
			}
			delete(s.sessions, id)
			changed = true
		}
	}
	if changed {
		s.wake()
	}
	return err
}
func expiredSummary(v Session) bool {
	days := 30
	if n, e := strconv.Atoi(os.Getenv("AGENTS_RETENTION_DAYS")); e == nil && n > 0 && n <= 3650 {
		days = n
	}
	return v.Execution != "working" && !IsAttention(v.Attention) && v.RequestKind != "question" && v.RequestKind != "permission" && v.RequestKind != "failure" && !v.LastActivity.IsZero() && time.Since(v.LastActivity) > time.Duration(days)*24*time.Hour
}

package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type RemoteConfig struct {
	URL             string `json:"url"`
	Token           string `json:"token"`
	DeviceID        string `json:"deviceID"`
	HostID          string `json:"hostID,omitempty"`
	KeychainAccount string `json:"keychainAccount,omitempty"`
}

// Bind enrolled credentials to a machine, never to a container image. The
// fingerprint stays local and is not included in exported session metadata.
func machineIdentity() string {
	if id := os.Getenv("AGENTS_MACHINE_ID"); id != "" {
		return Hash(id)
	}
	if runtime.GOOS == "linux" {
		if b, e := os.ReadFile("/etc/machine-id"); e == nil && len(strings.TrimSpace(string(b))) > 0 {
			return Hash(strings.TrimSpace(string(b)))
		}
	}
	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if b, e := exec.CommandContext(ctx, "/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output(); e == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.Contains(line, "\"IOPlatformUUID\"") {
					return Hash(strings.TrimSpace(line))
				}
			}
		}
	}
	// Containers without a machine-id must supply a stable unique identity;
	// images must not bake this value or an enrolled data directory in.
	return ""
}

type Transfer struct {
	ID       string  `json:"id"`
	Sequence int64   `json:"sequence"`
	Session  Session `json:"session"`
}
type Batch struct {
	Events []Transfer `json:"events"`
}

func remoteURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid hub URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) {
		return errors.New("hub requires HTTPS, except on loopback")
	}
	return nil
}
func request(ctx context.Context, client *http.Client, method, path, token string, input, output any) error {
	var body []byte
	if input != nil {
		body = wireJSON(input)
	}
	r, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	res, err := client.Do(r)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("hub returned HTTP %d", res.StatusCode)
	}
	if output != nil {
		return json.NewDecoder(res.Body).Decode(output)
	}
	return nil
}
func SyncRemote(ctx context.Context, s *Store, dir string) {
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	var nextAttempt time.Time
	backoff := 3 * time.Second
	configuration := ""
	connectivity := map[string]string{}
	hostID := machineIdentity()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if time.Now().Before(nextAttempt) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, "remote.json"))
		if err != nil {
			continue
		}
		var c RemoteConfig
		if json.Unmarshal(b, &c) != nil || remoteURL(c.URL) != nil {
			continue
		}
		if hostID == "" || c.HostID != hostID {
			s.Health(Health{ID: "hub", Name: "Remote hub", Status: "unavailable", Detail: "Machine identity changed or unavailable. Enroll this machine with a fresh token."})
			continue
		}
		if c.KeychainAccount != "" {
			c.Token, err = loadCredential(c.KeychainAccount)
			if err != nil {
				s.Health(Health{ID: "hub", Name: "Remote hub", Status: "unavailable", Detail: err.Error()})
				continue
			}
		}
		base := strings.TrimRight(c.URL, "/")
		// A newly paired collector exports its current projection even if its
		// earlier event history has expired. Subsequent reachability changes are
		// also events, independent of whether a provider wrote another turn.
		if configuration != string(b) {
			configuration = string(b)
			connectivity = map[string]string{}
		}
		for _, session := range s.Snapshot().Sessions {
			if session.Remote {
				continue
			}
			if previous, ok := connectivity[session.ID]; !ok || previous != session.Connectivity {
				if _, e := s.DB.Exec("INSERT INTO events(at,session_id,body,sent) VALUES(?,?,?,0)", time.Now().Unix(), session.ID, wireJSON(session)); e == nil {
					connectivity[session.ID] = session.Connectivity
				}
			}
		}
		rows, err := s.DB.Query("SELECT seq,body FROM events WHERE sent=0 ORDER BY seq LIMIT 50")
		if err != nil {
			continue
		}
		batch := Batch{Events: []Transfer{}}
		var seqs []int64
		for rows.Next() {
			var seq int64
			var data []byte
			var session Session
			if rows.Scan(&seq, &data) == nil && json.Unmarshal(data, &session) == nil && !session.Remote {
				session = PublicSession(session)
				session.MachineID = c.DeviceID
				session.ID = Key(c.DeviceID, session.Provider, session.NativeID)
				batch.Events = append(batch.Events, Transfer{ID: fmt.Sprintf("%s:%d", s.MachineID, seq), Sequence: seq, Session: session})
				seqs = append(seqs, seq)
			}
		}
		rows.Close()
		err = request(ctx, client, "POST", base+"/v1/ingest", c.Token, batch, nil)
		if err == nil {
			for _, seq := range seqs {
				_, _ = s.DB.Exec("UPDATE events SET sent=1 WHERE seq=?", seq)
			}
		}
		if err == nil {
			var snapshot Snapshot
			err = request(ctx, client, "GET", base+"/v1/sessions", c.Token, nil, &snapshot)
			if err == nil {
				present := map[string]bool{}
				for _, v := range snapshot.Sessions {
					if v.MachineID == c.DeviceID {
						continue
					}
					present[v.ID] = true
					v.Remote = true
					_ = s.Apply(v)
				}
				s.RemoveAbsentRemote(present)
			}
		}
		if err != nil {
			s.Health(Health{ID: "hub", Name: "Remote hub", Status: "unavailable", Detail: Clip(err.Error(), 150)})
			nextAttempt = time.Now().Add(backoff + time.Duration(rand.Int64N(int64(backoff/2))))
			backoff = min(backoff*2, time.Minute)
		} else {
			backoff = 3 * time.Second
			nextAttempt = time.Time{}
			s.Health(Health{ID: "hub", Name: "Remote hub", Status: "online", Detail: "Connected · remote summaries only"})
		}
	}
}
func HubAPI(s *Store, admin string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/enroll", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Token string `json:"token"`
			Name  string `json:"name"`
		}
		if readJSON(r, &v) != nil || len(v.Name) > 100 {
			http.Error(w, "invalid enrollment", 400)
			return
		}
		tx, e := s.DB.Begin()
		if e != nil {
			http.Error(w, "storage error", 500)
			return
		}
		defer tx.Rollback()
		result, e := tx.Exec("DELETE FROM enrollments WHERE token_hash=? AND expires>?", Hash(v.Token), time.Now().Unix())
		if e != nil {
			http.Error(w, "storage error", 500)
			return
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			http.Error(w, "invalid or expired enrollment", 401)
			return
		}
		id, token := ID(), ID()
		_, e = tx.Exec("INSERT INTO devices(id,token_hash,name,last_seen) VALUES(?,?,?,?)", id, Hash(token), v.Name, time.Now().Unix())
		if e != nil || tx.Commit() != nil {
			http.Error(w, "storage error", 500)
			return
		}
		respond(w, RemoteConfig{Token: token, DeviceID: id})
	})
	mux.Handle("POST /v1/admin/enrollment", Authorized(admin, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ID()
		if _, e := s.DB.Exec("INSERT INTO enrollments VALUES(?,?)", Hash(token), time.Now().Add(10*time.Minute).Unix()); e != nil {
			http.Error(w, "storage error", 500)
			return
		}
		respond(w, map[string]string{"token": token})
	})))
	mux.Handle("POST /v1/admin/revoke/{id}", Authorized(admin, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, e := s.DB.Exec("UPDATE devices SET revoked=1 WHERE id=?", r.PathValue("id")); e != nil {
			http.Error(w, "storage error", 500)
			return
		}
		respond(w, map[string]bool{"ok": true})
	})))
	mux.Handle("DELETE /v1/admin/sessions/{id}", Authorized(admin, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.DeleteRemote(r.PathValue("id")); err != nil {
			http.Error(w, "delete failed", 500)
			return
		}
		respond(w, map[string]bool{"ok": true})
	})))
	deviceAuth := func(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Origin") != "" {
				http.Error(w, "forbidden", 403)
				return
			}
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			var id string
			if len(token) < 20 || s.DB.QueryRow("SELECT id FROM devices WHERE token_hash=? AND revoked=0", Hash(token)).Scan(&id) != nil {
				http.Error(w, "unauthorized", 401)
				return
			}
			next(w, r, id)
		}
	}
	mux.HandleFunc("POST /v1/ingest", deviceAuth(func(w http.ResponseWriter, r *http.Request, id string) {
		var batch Batch
		if readJSON(r, &batch) != nil || len(batch.Events) > 100 {
			http.Error(w, "invalid batch", 400)
			return
		}
		for _, v := range batch.Events {
			if v.Sequence < 1 || v.Session.MachineID != id || v.Session.ID != Key(id, v.Session.Provider, v.Session.NativeID) || len(v.ID) > 200 {
				http.Error(w, "invalid session identity", 403)
				return
			}
		}
		for _, v := range batch.Events {
			if err := s.Ingest(id, v); err != nil {
				http.Error(w, "storage error", 500)
				return
			}
		}
		_, _ = s.DB.Exec("UPDATE devices SET last_seen=? WHERE id=?", time.Now().Unix(), id)
		respond(w, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("GET /v1/sessions", deviceAuth(func(w http.ResponseWriter, r *http.Request, id string) {
		v := s.Snapshot()
		last := map[string]int64{}
		rows, e := s.DB.Query("SELECT id,last_seen FROM devices WHERE revoked=0")
		if e == nil {
			for rows.Next() {
				var id string
				var at int64
				_ = rows.Scan(&id, &at)
				last[id] = at
			}
			rows.Close()
		}
		for i := range v.Sessions {
			session := &v.Sessions[i]
			if raw, ok := s.Get(session.ID); ok {
				session.Connectivity = raw.Connectivity
			}
			at, ok := last[session.MachineID]
			if !ok {
				session.Connectivity = "offline"
			} else {
				session.ObservedAt = time.Unix(at, 0).UTC()
				age := time.Since(session.ObservedAt)
				if age > 2*time.Minute {
					session.Connectivity = "offline"
				} else if age > 45*time.Second {
					session.Connectivity = "stale"
				}
			}
		}
		respond(w, v)
	}))
	return mux
}
func RunHub(ctx context.Context, o Options) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s, e := OpenStore(o.DataDir, "Hub")
	if e != nil {
		return e
	}
	defer s.DB.Close()
	var maintenance sync.WaitGroup
	maintenance.Add(1)
	go func() {
		defer maintenance.Done()
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				_ = s.Prune()
			}
		}
	}()
	defer func() { cancel(); maintenance.Wait() }()
	p := filepath.Join(o.DataDir, "admin-token")
	b, e := os.ReadFile(p)
	if os.IsNotExist(e) {
		b = []byte(ID())
		e = AtomicWrite(p, b)
	}
	if e != nil {
		return e
	}
	server := &http.Server{Addr: o.Listen, Handler: HubAPI(s, string(b)), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, MaxHeaderBytes: 8192}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(c)
	}()
	e = server.ListenAndServe()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
func Enroll(ctx context.Context, dir, base, token, name string) error {
	if e := remoteURL(base); e != nil {
		return e
	}
	hostID := machineIdentity()
	if hostID == "" {
		return errors.New("no stable machine identity: set AGENTS_MACHINE_ID to a unique persistent value before enrolling")
	}
	var c RemoteConfig
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if e := request(ctx, client, "POST", strings.TrimRight(base, "/")+"/v1/enroll", "", map[string]string{"token": token, "name": name}, &c); e != nil {
		return e
	}
	c.URL = base
	c.HostID = hostID
	account, err := saveCredential(dir, c.Token)
	if err != nil {
		return err
	}
	if account != "" {
		c.KeychainAccount = account
		c.Token = ""
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	return AtomicWrite(filepath.Join(dir, "remote.json"), wireJSON(c))
}

// The receipt, projection and per-session ordering watermark commit together.
func (s *Store) Ingest(device string, v Transfer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var deleted int
	if e = tx.QueryRow("SELECT count(*) FROM tombstones WHERE session_id=?", v.Session.ID).Scan(&deleted); e != nil {
		return e
	}
	if deleted > 0 {
		return nil
	}
	var seq int64
	e = tx.QueryRow("SELECT seq FROM watermarks WHERE device=? AND session_id=?", device, v.Session.ID).Scan(&seq)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if v.Sequence <= seq {
		return nil
	}
	session := PublicSession(v.Session)
	session.Remote = true
	session.ObservedAt = time.Now().UTC()
	body := wireJSON(session)
	if _, e = tx.Exec("INSERT INTO sessions VALUES(?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body", session.ID, body); e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT INTO watermarks VALUES(?,?,?) ON CONFLICT(device,session_id) DO UPDATE SET seq=excluded.seq", device, session.ID, v.Sequence); e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT OR IGNORE INTO receipts VALUES(?,?)", device, v.ID); e != nil {
		return e
	}
	r, e := tx.Exec("INSERT INTO events(at,session_id,body,sent) VALUES(?,?,?,1)", time.Now().Unix(), session.ID, body)
	if e != nil {
		return e
	}
	eventSeq, _ := r.LastInsertId()
	if e = tx.Commit(); e != nil {
		return e
	}
	s.sessions[session.ID] = session
	s.seq = eventSeq
	s.wake()
	return nil
}

func (s *Store) DeleteRemote(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.sessions[id]
	if !ok {
		return nil
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("INSERT OR IGNORE INTO tombstones VALUES(?,?)", id, v.MachineID); e != nil {
		return e
	}
	if _, e = tx.Exec("DELETE FROM sessions WHERE id=?", id); e != nil {
		return e
	}
	if _, e = tx.Exec("DELETE FROM events WHERE session_id=?", id); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	delete(s.sessions, id)
	s.wake()
	return nil
}
func (s *Store) RemoveAbsentRemote(present map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for id, v := range s.sessions {
		if v.Remote && !present[id] {
			if _, e := s.DB.Exec("DELETE FROM sessions WHERE id=?", id); e == nil {
				delete(s.sessions, id)
				changed = true
			}
		}
	}
	if changed {
		s.wake()
	}
}

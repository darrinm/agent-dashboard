package core

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type Options struct{ Home, DataDir, Claude, Codex, Machine, Listen string }
type Endpoint struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

func DefaultDir() string {
	home, _ := os.UserHomeDir()
	if v := os.Getenv("AGENTS_DATA_DIR"); v != "" {
		return v
	}
	if runtime.GOOS != "darwin" {
		return filepath.Join(home, ".local", "state", "agents")
	}
	return filepath.Join(home, "Library", "Application Support", "Agents")
}
func AtomicWrite(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".agents-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	_ = f.Chmod(0600)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}
func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 2*1024*1024+1))
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("expected one JSON document")
	}
	return nil
}
func respond(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
func Authorized(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if origin := r.Header.Get("Origin"); origin != "" {
			http.Error(w, "browser requests are not allowed", http.StatusForbidden)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if len(token) == 0 || subtle.ConstantTimeCompare([]byte(token), []byte(got)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func LocalAPI(store *Store, discovery *Discovery) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions", func(w http.ResponseWriter, r *http.Request) { respond(w, store.Snapshot()) })
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) { respond(w, store.Snapshot().Sources) })
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "stream unsupported", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		ch, done := store.Subscribe()
		defer done()
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		send := func() bool {
			v := store.Snapshot()
			_, err := fmt.Fprintf(w, "id: %d\nevent: snapshot\ndata: %s\n\n", v.Sequence, wireJSON(v))
			f.Flush()
			return err == nil
		}
		if !send() {
			return
		}
		last := time.Now()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ch:
				// Coalesce bursts (for example hook events from active sessions) into
				// at most one full snapshot per second.
				if wait := time.Second - time.Since(last); wait > 0 {
					select {
					case <-time.After(wait):
					case <-r.Context().Done():
						return
					}
				}
				select { // This send covers any change signalled during the wait.
				case <-ch:
				default:
				}
				if !send() {
					return
				}
				last = time.Now()
			case <-tick.C:
				if !send() {
					return
				}
			}
		}
	})
	mux.HandleFunc("POST /v1/attention", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			SessionID string `json:"sessionID"`
			EpisodeID string `json:"episodeID"`
			Action    string `json:"action"`
		}
		if readJSON(r, &req) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		if err := store.Acknowledge(req.SessionID, req.EpisodeID, req.Action); err != nil {
			switch {
			case errors.Is(err, ErrStaleEpisode):
				http.Error(w, err.Error(), http.StatusConflict)
			case errors.Is(err, ErrInvalidAcknowledgment):
				http.Error(w, err.Error(), http.StatusBadRequest)
			default:
				http.Error(w, "Could not save acknowledgment", http.StatusInternalServerError)
			}
			return
		}
		respond(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /v1/feedback", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			SessionID string `json:"sessionID"`
			Verdict   string `json:"verdict"`
			Note      string `json:"note"`
		}
		if readJSON(r, &req) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		if err := store.RecordFeedback(req.SessionID, req.Verdict, req.Note); err != nil {
			switch {
			case errors.Is(err, ErrUnknownSession):
				http.Error(w, err.Error(), http.StatusNotFound)
			case errors.Is(err, ErrInvalidFeedback):
				http.Error(w, err.Error(), http.StatusBadRequest)
			default:
				http.Error(w, "Could not save feedback", http.StatusInternalServerError)
			}
			return
		}
		respond(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /v1/hooks/{provider}", func(w http.ResponseWriter, r *http.Request) {
		var event map[string]any
		if readJSON(r, &event) != nil {
			http.Error(w, "invalid hook", 400)
			return
		}
		provider := r.PathValue("provider")
		if provider != "claude" && provider != "codex" {
			http.Error(w, "unknown provider", 400)
			return
		}
		at := time.Now().UTC()
		result := ApplyHook(store, provider, event, at)
		if discovery != nil {
			discovery.HookReceived(provider, event, result, at)
		}
		respond(w, map[string]bool{"ok": true})
	})
	return mux
}
func RunCollector(ctx context.Context, o Options) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	store, err := OpenStore(o.DataDir, o.Machine)
	if err != nil {
		return err
	}
	defer store.DB.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	endpoint := Endpoint{URL: "http://" + listener.Addr().String(), Token: ID(), PID: os.Getpid()}
	if err = AtomicWrite(filepath.Join(o.DataDir, "collector.json"), wireJSON(endpoint)); err != nil {
		return err
	}
	d := &Discovery{Store: store, Home: o.Home, Claude: o.Claude, Codex: o.Codex, Wake: make(chan struct{}, 1)}
	store.Health(Health{ID: "claude-cloud", Name: "Claude cloud", Status: "unsupported", Detail: "Account-wide session discovery is not available"})
	store.Health(Health{ID: "codex-cloud", Name: "Codex Cloud", Status: "unsupported", Detail: "Managed cloud task discovery is not enabled. Cloud VM sessions are covered by remote collectors."})
	store.Health(Health{ID: "app-server", Name: "Codex App Server", Status: "unsupported", Detail: "No verified shared-daemon connection; rollout fallback active"})
	server := &http.Server{Handler: Authorized(endpoint.Token, LocalAPI(store, d)), ReadHeaderTimeout: 3 * time.Second, IdleTimeout: time.Minute}
	var workers sync.WaitGroup
	defer func() {
		cancel()
		workers.Wait()
		if d.codexDB != nil {
			d.codexDB.Close()
		}
	}()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(c)
	}()
	workers.Add(1)
	go func() {
		defer workers.Done()
		d.Refresh(ctx)
		watcher, e := fsnotify.NewWatcher()
		if e == nil {
			defer watcher.Close()
			for _, base := range []string{filepath.Join(o.Home, ".claude", "sessions"), filepath.Join(o.Home, ".claude", "projects"), filepath.Join(o.Home, ".codex")} {
				_ = watcher.Add(base)
			}
		}
		var events <-chan fsnotify.Event
		var errs <-chan error
		if watcher != nil {
			events = watcher.Events
			errs = watcher.Errors
		}
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		last := time.Time{}
		dirty := true
		n := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-d.Wake:
				dirty = true
			case event, ok := <-events:
				if !ok {
					events = nil
					break
				}
				if d.NoteFileEvent(event.Name) {
					dirty = true
				}
			case _, ok := <-errs:
				// Keep draining: fsnotify stops delivering events while an error is unread.
				if !ok {
					errs = nil
				}
			case <-tick.C:
				// File events are hints; reconcile everything periodically regardless.
				if dirty && time.Since(last) >= time.Second || time.Since(last) >= reconcileInterval {
					// Stay dirty while a requested inventory is deferred.
					dirty = d.Refresh(ctx)
					last = time.Now()
					if watcher != nil {
						d.mu.Lock()
						for path := range d.tails {
							_ = watcher.Add(filepath.Dir(path))
						}
						d.mu.Unlock()
					}
				}
				n++
				if n%60 == 0 {
					_ = store.Prune()
				}
			}
		}
	}()
	workers.Add(1)
	go func() { defer workers.Done(); SyncRemote(ctx, store, o.DataDir) }()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
// HookResult tells the caller of ApplyHook what else a hook calls for.
type HookResult int

const (
	HookApplied        HookResult = iota
	HookUnknownSession            // the store doesn't have the session yet
	HookNeedsInventory            // the event can change what Claude's inventory reports
)

// lifecycleEvent reports whether a Claude hook event can change what inventory
// reports, as opposed to per-turn events such as tool use.
func lifecycleEvent(name string) bool {
	switch name {
	case "SessionStart", "SessionEnd", "Stop", "Notification", "PermissionRequest":
		return true
	}
	return false
}

// ApplyHook applies a lifecycle event that happened at the given time to a known session.
func ApplyHook(store *Store, provider string, m map[string]any, at time.Time) HookResult {
	id := str(m, "session_id")
	event := str(m, "hook_event_name")
	if provider == "codex" {
		id, event = str(m, "thread-id"), str(m, "type")
	}
	if id == "" {
		return HookApplied
	}
	s, ok := store.Get(Key(store.MachineID, provider, id))
	if !ok {
		return HookUnknownSession
	}
	result := HookApplied
	if provider == "claude" && lifecycleEvent(event) {
		result = HookNeedsInventory
	}
	previousHook := s.HookAt
	s.Evidence = "provider event"
	s.ObservedAt = time.Now().UTC()
	s.HookAt = at
	switch event {
	case "Notification":
		typ := str(m, "notification_type")
		if typ != "permission_prompt" && typ != "elicitation_dialog" && typ != "agent_needs_input" {
			return result
		}
		kind := "question"
		if typ == "permission_prompt" {
			kind = "permission"
		}
		message := Clip(str(m, "message"), 600)
		// A prompt announced again with no activity in between keeps its episode.
		// A different prompt, or any prompt after the last one resolved, is new,
		// even when transcript activity hasn't been read yet.
		repeated := s.RequestKind == kind && strings.HasPrefix(s.RequestKey, "hook:") && s.Summary == message &&
			!previousHook.IsZero() && !s.LastActivity.After(previousHook)
		s.RequestKind = kind
		if !repeated {
			s.RequestKey = "hook:" + typ + ":" + at.Format(time.RFC3339Nano)
			s.StateSince = at
		}
		s.Summary = message
	case "PermissionRequest":
		request := str(m, "tool_use_id")
		if request == "" {
			request = at.Format(time.RFC3339Nano)
		}
		s.RequestKind = "permission"
		s.RequestKey = "hook:" + request
		s.StateSince = at
		s.Summary = "Permission requested for " + str(m, "tool_name")
	case "UserPromptSubmit":
		s.Execution = "working"
		s.RequestKind = ""
		s.StateSince = at
		s.LastActivity = at
	case "PostToolUse":
		s.Execution = "working"
		s.RequestKind = ""
		s.LastActivity = at
	case "SessionEnd":
		s.Execution = "ended"
		s.RequestKind = ""
		s.LastActivity = at
	default:
		// SessionStart also fires on resume, /clear and /compact, which don't start a
		// turn, and completion hooks don't say whether the result is a question or
		// work to review. The refresh these events request decides.
		return result
	}
	_ = store.Apply(s)
	return result
}

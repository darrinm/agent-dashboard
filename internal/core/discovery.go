package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Discovery struct {
	Store               *Store
	Home, Claude, Codex string
	Wake                chan struct{}

	// mu guards refresh state; Refresh holds it for its whole run.
	mu              sync.Mutex
	tails           map[string]*Tail
	claudeInventory []map[string]any
	lastInventory   time.Time
	claudeError     error
	codexDB         *sql.DB
	lastCodex       time.Time

	// Hook and file-event handlers set these without waiting for a refresh, so a
	// hook never stalls the agent that sent it.
	inventoryRequested atomic.Bool // a Claude inventory sooner than reconcileInterval
	codexDirty         atomic.Bool // Codex's thread database needs re-reading
	hookMu             sync.Mutex
	pending            []queuedHook
}

const (
	// File events are hints; every source is reconciled at least this often.
	reconcileInterval = 15 * time.Second
	// inventoryMinInterval bounds how often lifecycle events can force `claude agents` to run.
	inventoryMinInterval = 3 * time.Second
	// inventoryLag is how far behind Claude's inventory may be, so a "working" or
	// "done" state only vouches for the session from that long before it was taken.
	inventoryLag    = 5 * time.Second
	hookQueueWindow = 2 * time.Minute
)

type queuedHook struct {
	provider string
	event    map[string]any
	at       time.Time
}

// hookFields are the payload fields ApplyHook reads. Queued events keep only
// these, because tool payloads can be large.
var hookFields = []string{"session_id", "thread-id", "hook_event_name", "type", "notification_type", "message", "tool_use_id", "tool_name"}

// HookReceived schedules the work a hook calls for after ApplyHook has run.
func (d *Discovery) HookReceived(provider string, event map[string]any, result HookResult, at time.Time) {
	switch {
	case provider == "codex":
		d.codexDirty.Store(true)
	case result == HookUnknownSession && lifecycleEvent(str(event, "hook_event_name")):
		// A new session can raise a prompt before inventory lists it; replay it after.
		trimmed := map[string]any{}
		for _, k := range hookFields {
			if v, ok := event[k]; ok {
				trimmed[k] = v
			}
		}
		d.hookMu.Lock()
		if len(d.pending) >= 200 {
			d.pending = d.pending[1:]
		}
		d.pending = append(d.pending, queuedHook{provider: provider, event: trimmed, at: at})
		d.hookMu.Unlock()
		d.inventoryRequested.Store(true)
	case result == HookNeedsInventory:
		d.inventoryRequested.Store(true)
	}
	select {
	case d.Wake <- struct{}{}:
	default:
	}
}
func (d *Discovery) replayHooks() {
	d.hookMu.Lock()
	queued := d.pending
	d.pending = nil
	d.hookMu.Unlock()
	var keep []queuedHook
	for _, h := range queued {
		if ApplyHook(d.Store, h.provider, h.event, h.at) == HookUnknownSession && time.Since(h.at) < hookQueueWindow {
			keep = append(keep, h)
		}
	}
	if len(keep) > 0 {
		d.hookMu.Lock()
		d.pending = append(keep, d.pending...)
		d.hookMu.Unlock()
	}
}

// NoteFileEvent reports whether a watched file change can affect session state,
// and marks Codex's database for re-reading when it can. Codex constantly writes
// other files under ~/.codex, such as its logs database.
func (d *Discovery) NoteFileEvent(name string) bool {
	rel, err := filepath.Rel(filepath.Join(d.Home, ".codex"), name)
	if err != nil || strings.HasPrefix(rel, "..") {
		return true
	}
	if !strings.HasPrefix(rel, "sessions"+string(filepath.Separator)) && !strings.HasPrefix(rel, "state_") {
		return false
	}
	d.codexDirty.Store(true)
	return true
}

// codexAbandonedAfter is how long a Codex turn can go without recorded activity
// before it is presumed dead: longer while a tool call is still open.
func codexAbandonedAfter(openTools int) time.Duration {
	if openTools > 0 {
		return 6 * time.Hour
	}
	return 30 * time.Minute
}

// providerMissing reports whether a provider's executable doesn't exist, which is
// a permanent condition rather than a source that stopped reporting.
func providerMissing(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist)
}

func (d *Discovery) tail(path, provider string) *Tail {
	if d.tails == nil {
		d.tails = map[string]*Tail{}
	}
	t, ok := d.tails[path]
	if !ok {
		t = newTail()
		d.Store.LoadTail(path, t)
		d.tails[path] = t
	}
	if changed, err := t.Read(path, provider); err == nil && changed {
		d.Store.SaveTail(path, t)
	}
	return t
}
// Refresh reconciles every source. It reports whether a requested inventory is
// still outstanding because inventoryMinInterval deferred it.
func (d *Discovery) Refresh(ctx context.Context) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.claude(ctx)
	d.codex(ctx)
	d.replayHooks()
	return d.inventoryRequested.Load()
}
func (d *Discovery) base(provider, id, cwd, title, kind string) Session {
	return Session{ID: Key(d.Store.MachineID, provider, id), NativeID: id, MachineID: d.Store.MachineID, Machine: d.Store.Machine, Provider: provider, Source: provider, Kind: kind, Title: title, Project: Project(cwd), Cwd: cwd, Execution: "unknown", Outcome: "unknown", Connectivity: "online", Evidence: "snapshot", ObservedAt: time.Now().UTC(), Capabilities: []string{"read_history"}, Aliases: []string{}, Buckets: []Bucket{}}
}
func (d *Discovery) enrich(s *Session, t *Tail) {
	s.LastActivity = t.LastActivity
	s.StateSince = t.LastTurn
	s.Summary = t.Summary
	s.Branch = t.Branch
	s.Outcome = t.Outcome
	s.RequestKind = t.RequestKind
	s.RequestKey = t.RequestKey
	s.HasActivity = t.HasActivity
	s.Buckets, s.OpenTools = t.Buckets(time.Now())
	if t.Alias != "" {
		s.Aliases = append(s.Aliases, t.Alias)
	}
}
func (d *Discovery) claude(ctx context.Context) {
	since := time.Since(d.lastInventory)
	refreshed := false
	if since > reconcileInterval || d.inventoryRequested.Load() && since > inventoryMinInterval {
		d.inventoryRequested.Store(false)
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		out, err := exec.CommandContext(c, d.Claude, "agents", "--json", "--all").Output()
		cancel()
		d.lastInventory = time.Now()
		d.claudeError = err
		if err == nil {
			var a []map[string]any
			if err = json.Unmarshal(out, &a); err == nil {
				d.claudeInventory = a
				refreshed = true
			} else {
				d.claudeError = err
			}
		}
	}
	if d.claudeError != nil {
		status, detail := "unavailable", "Session inventory unavailable: "+Clip(d.claudeError.Error(), 200)
		if providerMissing(d.claudeError) {
			status, detail = "unsupported", "Claude Code isn't installed on this machine"
		}
		d.Store.Health(Health{ID: "claude", Name: "Claude Code", Status: status, Detail: detail})
		return
	}
	files, _ := filepath.Glob(filepath.Join(d.Home, ".claude", "sessions", "*.json"))
	live := map[string]map[string]any{}
	for _, f := range files {
		b, e := os.ReadFile(f)
		if e != nil {
			continue
		}
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			live[str(m, "sessionId")] = m
		}
	}
	for _, m := range d.claudeInventory {
		id := str(m, "sessionId")
		if id == "" {
			continue
		}
		s := d.base("claude", id, str(m, "cwd"), str(m, "name"), str(m, "kind"))
		if s.Title == "" {
			s.Title = s.Project
		}
		encoded := strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
				return r
			}
			return '-'
		}, s.Cwd)
		path := filepath.Join(d.Home, ".claude", "projects", encoded, id+".jsonl")
		if _, err := os.Stat(path); err != nil {
			matches, _ := filepath.Glob(filepath.Join(d.Home, ".claude", "projects", "*", id+".jsonl"))
			if len(matches) > 0 {
				path = matches[0]
			}
		}
		t := d.tail(path, "claude")
		d.enrich(&s, t)
		s.Transcript = path
		s.Execution = t.Execution
		if s.StateSince.IsZero() {
			s.StateSince = time.UnixMilli(int64(num(m, "startedAt"))).UTC()
			s.Evidence = "inferred"
		}
		if s.LastActivity.IsZero() {
			s.LastActivity = s.StateSince
		}
		state := str(m, "state")
		switch state {
		case "working":
			s.Execution = "working"
			s.RequestKind = ""
			s.Outcome = "unknown"
			s.notWaitingAt = d.lastInventory.Add(-inventoryLag)
		case "blocked":
			s.Execution = "idle"
			if s.RequestKind != "failure" {
				s.RequestKind = "question"
				if s.RequestKey == "" {
					s.RequestKey = t.LastUser.Format(time.RFC3339Nano) + ":" + t.LastTurn.Format(time.RFC3339Nano)
				}
			}
		case "done":
			s.Execution = "ended"
			s.notWaitingAt = d.lastInventory.Add(-inventoryLag)
			if s.Outcome != "failed" {
				s.Outcome = "completed"
				s.RequestKind = "review"
				s.RequestKey = s.StateSince.Format(time.RFC3339Nano)
			}
		}
		if record := live[id]; record != nil {
			if alias := str(record, "bridgeSessionId"); alias != "" {
				s.Aliases = []string{alias}
			}
			if state == "" {
				switch str(record, "status") {
				case "busy":
					s.Execution = "working"
				case "idle":
					s.Execution = "idle"
				}
				s.Evidence = "snapshot"
			}
		}
		if s.Kind == "background" && state != "done" {
			s.AttachID = str(m, "id")
			s.Capabilities = append(s.Capabilities, "attach")
		}
		if s.Cwd != "" {
			s.Capabilities = append(s.Capabilities, "open_project")
		}
		s.Capabilities = append(s.Capabilities, "observe_live")
		if s.Execution != "working" || s.RequestKind != "" {
			s.Buckets = []Bucket{}
		}
		if expiredSummary(s) {
			delete(d.tails, path)
			continue
		}
		if err := d.Store.Apply(s); err != nil {
			d.Store.Health(Health{ID: "storage", Name: "Storage", Status: "error", Detail: err.Error()})
		}
	}
	if refreshed {
		d.endVanishedClaudeSessions()
	}
	d.Store.Health(Health{ID: "claude", Name: "Claude Code", Status: "online", Detail: fmt.Sprintf("%d sessions · inventory, session files and transcript events", len(d.claudeInventory))})
}

// endVanishedClaudeSessions ends local Claude sessions that inventory no longer
// lists, such as removed or crashed sessions. Without this, a session last seen
// working or waiting would keep that state until retention expires, or forever.
func (d *Discovery) endVanishedClaudeSessions() {
	if len(d.claudeInventory) == 0 {
		return // An empty inventory is more likely a transient failure than no sessions at all.
	}
	listed := map[string]bool{}
	for _, m := range d.claudeInventory {
		listed[Key(d.Store.MachineID, "claude", str(m, "sessionId"))] = true
	}
	now := time.Now().UTC()
	vanished := d.Store.Sessions(func(s Session) bool {
		settled := s.Execution == "ended" && (s.RequestKind == "" || s.RequestKind == "review")
		return !s.Remote && s.Provider == "claude" && s.MachineID == d.Store.MachineID && !listed[s.ID] && !settled
	})
	for _, s := range vanished {
		s.Execution = "ended"
		if s.RequestKind != "review" {
			s.RequestKind, s.RequestKey = "", ""
		}
		s.Buckets, s.OpenTools = []Bucket{}, 0
		s.Evidence = "snapshot"
		s.ObservedAt = now
		s.notWaitingAt = d.lastInventory // A session that has gone can't be waiting.
		_ = d.Store.Apply(s)
	}
}
func (d *Discovery) codex(ctx context.Context) {
	if !d.codexDirty.Swap(false) && time.Since(d.lastCodex) < reconcileInterval {
		return
	}
	d.lastCodex = time.Now()
	if d.codexDB == nil {
		paths, _ := filepath.Glob(filepath.Join(d.Home, ".codex", "state_*.sqlite"))
		if len(paths) == 0 {
			d.Store.Health(Health{ID: "codex", Name: "Codex", Status: "unsupported", Detail: "Codex isn't installed on this machine"})
			return
		}
		// Prefer the highest numeric schema, not lexical filename order.
		best := ""
		version := -1
		for _, p := range paths {
			var v int
			fmt.Sscanf(filepath.Base(p), "state_%d.sqlite", &v)
			if v > version {
				version = v
				best = p
			}
		}
		db, err := sql.Open("sqlite", "file:"+best+"?mode=ro&_pragma=busy_timeout(1000)")
		if err != nil {
			return
		}
		db.SetMaxOpenConns(1)
		d.codexDB = db
	}
	const threads = `SELECT id,rollout_path,cwd,title,source,COALESCE(git_branch,''),created_at,updated_at,%s FROM threads WHERE archived=0 ORDER BY updated_at DESC LIMIT 500`
	rows, err := d.codexDB.QueryContext(ctx, fmt.Sprintf(threads, "COALESCE(agent_nickname,'')"))
	if err != nil && strings.Contains(err.Error(), "no such column") {
		// Older schemas have no subagent nicknames.
		rows, err = d.codexDB.QueryContext(ctx, fmt.Sprintf(threads, "''"))
	}
	if err != nil {
		d.Store.Health(Health{ID: "codex", Name: "Codex", Status: "unavailable", Detail: "Unsupported or busy thread database: " + Clip(err.Error(), 160)})
		return
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, path, cwd, title, source, branch, nickname string
		var created, updated int64
		if rows.Scan(&id, &path, &cwd, &title, &source, &branch, &created, &updated, &nickname) != nil {
			continue
		}
		kind := source
		if strings.HasPrefix(source, "{") {
			if strings.Contains(source, "guardian") {
				continue
			}
			kind = "subagent"
		}
		if title == "" {
			switch {
			case nickname != "":
				title = nickname
			case kind == "subagent":
				title = "Subagent"
			}
		}
		s := d.base("codex", id, cwd, title, kind)
		s.Branch = branch
		s.Transcript = path
		s.Evidence = "inferred"
		t := d.tail(path, "codex")
		d.enrich(&s, t)
		s.Execution = t.Execution
		if s.Branch == "" {
			s.Branch = branch
		}
		if s.StateSince.IsZero() {
			s.StateSince = time.Unix(created, 0).UTC()
		}
		if s.LastActivity.IsZero() {
			s.LastActivity = time.Unix(updated, 0).UTC()
		}
		// Codex records no completion when its process dies mid-turn, so a turn that
		// has gone quiet for too long is no longer counted as working. Connectivity
		// describes the source, which is still reporting, so it isn't touched.
		if s.Execution == "working" && time.Since(s.LastActivity) > codexAbandonedAfter(s.OpenTools) {
			s.Execution = "unknown"
		}
		if s.Outcome == "completed" && s.RequestKind == "" {
			s.RequestKind = "review"
			s.RequestKey = s.StateSince.Format(time.RFC3339Nano)
		}
		if s.Execution == "unknown" && time.Since(s.LastActivity) > 7*24*time.Hour {
			continue
		}
		if kind == "subagent" {
			var src map[string]any
			if json.Unmarshal([]byte(source), &src) == nil {
				p := str(obj(obj(src, "subagent"), "thread_spawn"), "parent_thread_id")
				if p != "" {
					s.ParentID = Key(d.Store.MachineID, "codex", p)
				}
			}
		}
		if cwd != "" {
			s.Capabilities = append(s.Capabilities, "open_project")
		}
		if s.Execution != "working" || s.RequestKind != "" {
			s.Buckets = []Bucket{}
		}
		if expiredSummary(s) {
			delete(d.tails, path)
			continue
		}
		_ = d.Store.Apply(s)
		count++
	}
	d.Store.Health(Health{ID: "codex", Name: "Codex", Status: "degraded", Detail: fmt.Sprintf("%d threads · read-only rollout fallback; pending approvals may be unavailable", count)})
}

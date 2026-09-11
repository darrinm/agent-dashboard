package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Discovery struct {
	Store               *Store
	Home, Claude, Codex string
	mu                  sync.Mutex
	tails               map[string]*Tail
	claudeInventory     []map[string]any
	lastInventory       time.Time
	claudeError         error
	codexDB             *sql.DB
	Wake                chan struct{}
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
func (d *Discovery) Refresh(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.claude(ctx)
	d.codex(ctx)
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
	if time.Since(d.lastInventory) > 15*time.Second {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		out, err := exec.CommandContext(c, d.Claude, "agents", "--json", "--all").Output()
		cancel()
		d.lastInventory = time.Now()
		d.claudeError = err
		if err == nil {
			var a []map[string]any
			if err = json.Unmarshal(out, &a); err == nil {
				d.claudeInventory = a
			} else {
				d.claudeError = err
			}
		}
	}
	if d.claudeError != nil {
		d.Store.Health(Health{ID: "claude", Name: "Claude Code", Status: "unavailable", Detail: "Session inventory unavailable: " + Clip(d.claudeError.Error(), 200)})
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
	d.Store.Health(Health{ID: "claude", Name: "Claude Code", Status: "online", Detail: fmt.Sprintf("%d sessions · inventory, session files and transcript events", len(d.claudeInventory))})
}
func (d *Discovery) codex(ctx context.Context) {
	if d.codexDB == nil {
		paths, _ := filepath.Glob(filepath.Join(d.Home, ".codex", "state_*.sqlite"))
		if len(paths) == 0 {
			d.Store.Health(Health{ID: "codex", Name: "Codex", Status: "unavailable", Detail: "No local thread database found"})
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
	rows, err := d.codexDB.QueryContext(ctx, `SELECT id,rollout_path,cwd,title,source,COALESCE(git_branch,''),created_at,updated_at FROM threads WHERE archived=0 ORDER BY updated_at DESC LIMIT 500`)
	if err != nil {
		d.Store.Health(Health{ID: "codex", Name: "Codex", Status: "unavailable", Detail: "Unsupported or busy thread database: " + Clip(err.Error(), 160)})
		return
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, path, cwd, title, source, branch string
		var created, updated int64
		if rows.Scan(&id, &path, &cwd, &title, &source, &branch, &created, &updated) != nil {
			continue
		}
		kind := source
		if strings.HasPrefix(source, "{") {
			if strings.Contains(source, "guardian") {
				continue
			}
			kind = "subagent"
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
		if s.Execution == "working" && s.OpenTools == 0 && time.Since(s.LastActivity) > 2*time.Minute {
			s.Connectivity = "stale"
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

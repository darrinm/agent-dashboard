package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemoteCollectorsEndToEnd(t *testing.T) {
	t.Setenv("AGENTS_MACHINE_ID", "integration-test-machine")
	t.Setenv("AGENTS_CREDENTIAL_STORE", "file")
	hub := testStore(t)
	server := httptest.NewServer(HubAPI(hub, "admin-secret"))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := server.Client()
	enroll := func(dir string) RemoteConfig {
		t.Helper()
		var invitation map[string]string
		if e := request(ctx, client, "POST", server.URL+"/v1/admin/enrollment", "admin-secret", nil, &invitation); e != nil {
			t.Fatal(e)
		}
		if e := Enroll(ctx, dir, server.URL, invitation["token"], "Test machine"); e != nil {
			t.Fatal(e)
		}
		b, e := os.ReadFile(filepath.Join(dir, "remote.json"))
		if e != nil {
			t.Fatal(e)
		}
		var c RemoteConfig
		_ = json.Unmarshal(b, &c)
		return c
	}
	dirA, dirB := t.TempDir(), t.TempDir()
	a, e := OpenStore(dirA, "source")
	if e != nil {
		t.Fatal(e)
	}
	defer a.DB.Close()
	b, e := OpenStore(dirB, "viewer")
	if e != nil {
		t.Fatal(e)
	}
	defer b.DB.Close()
	config := enroll(dirA)
	enroll(dirB)
	v := testSession(a)
	v.Summary = "private-test-message"
	v.Cwd = "/private/path"
	if e = a.Apply(v); e != nil {
		t.Fatal(e)
	}
	// Pairing must backfill a projection even when there is no retained outbox.
	_, _ = a.DB.Exec("DELETE FROM events")
	done := make(chan struct{}, 2)
	go func() { SyncRemote(ctx, a, dirA); done <- struct{}{} }()
	go func() { SyncRemote(ctx, b, dirB); done <- struct{}{} }()
	defer func() { cancel(); <-done; <-done }()
	id := Key(config.DeviceID, v.Provider, v.NativeID)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if received, ok := b.Get(id); ok {
			if received.Attention == nil || !received.Remote || received.Cwd != "" || strings.Contains(received.Summary, "private") {
				t.Fatal("incorrect remote projection")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("remote session did not reach viewer")
}

func TestTailSkipsOversizedRecordsAndContinues(t *testing.T) {
	p := filepath.Join(t.TempDir(), "large.jsonl")
	_ = os.WriteFile(p, []byte("{}\n"), 0600)
	tail := newTail()
	_, _ = tail.Read(p, "codex")
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	_, _ = f.WriteString(strings.Repeat("x", 5*1024*1024) + "\n" + `{"type":"event_msg","timestamp":"2026-09-10T12:00:00Z","payload":{"type":"task_complete"}}` + "\n")
	f.Close()
	for i := 0; i < 3; i++ {
		if _, e := tail.Read(p, "codex"); e != nil {
			t.Fatal(e)
		}
	}
	if tail.Execution != "idle" || tail.Outcome != "completed" || !tail.PartialHistory {
		t.Fatal("oversized record blocked subsequent events")
	}
}

func TestOutboxHardByteLimit(t *testing.T) {
	s := testStore(t)
	pairStore(t, s)
	if _, e := s.DB.Exec("INSERT INTO events(at,session_id,body) VALUES(?,?,zeroblob(?))", time.Now().Unix(), "oversized", 101*1024*1024); e != nil {
		t.Fatal(e)
	}
	_, _ = s.DB.Exec("INSERT INTO events(at,session_id,body) VALUES(?,?,?)", time.Now().Unix(), "latest", []byte("latest"))
	if e := s.Prune(); e != nil {
		t.Fatal(e)
	}
	var size int64
	_ = s.DB.QueryRow("SELECT COALESCE(sum(length(body)),0) FROM events WHERE sent=0").Scan(&size)
	if size != 6 {
		t.Fatalf("bound failed or newest event was lost: %d", size)
	}
}

func TestHubEnrollmentReplayRevocationAndPrivacy(t *testing.T) {
	s := testStore(t)
	h := HubAPI(s, "admin-secret")
	call := func(method, path, token string, v any) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(wireJSON(v)))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	w := call("POST", "/v1/admin/enrollment", "admin-secret", nil)
	var invitation map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &invitation)
	w = call("POST", "/v1/enroll", "", map[string]string{"token": invitation["token"], "name": "Remote"})
	var device RemoteConfig
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &device) != nil {
		t.Fatal(w.Code)
	}
	if call("POST", "/v1/enroll", "", map[string]string{"token": invitation["token"], "name": "Other"}).Code != 401 {
		t.Fatal("enrollment reused")
	}
	v := testSession(s)
	v.MachineID = device.DeviceID
	v.ID = Key(device.DeviceID, "claude", v.NativeID)
	v.Summary = "private output"
	v.Cwd = "/private/path"
	v.Attention = &Episode{ID: "episode", Kind: "question", OpenedAt: time.Now().UTC()}
	one := Transfer{ID: "stream:1", Sequence: 1, Session: v}
	if call("POST", "/v1/ingest", device.Token, Batch{Events: []Transfer{one}}).Code != 200 {
		t.Fatal("ingest failed")
	}
	v.Execution = "working"
	v.Attention = nil
	two := Transfer{ID: "stream:2", Sequence: 2, Session: v}
	_ = call("POST", "/v1/ingest", device.Token, Batch{Events: []Transfer{two, one, two}})
	w = call("GET", "/v1/sessions", device.Token, nil)
	if strings.Contains(w.Body.String(), "private") {
		t.Fatal("private data uploaded")
	}
	var snapshot Snapshot
	_ = json.Unmarshal(w.Body.Bytes(), &snapshot)
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].Attention != nil {
		t.Fatal("replay regressed latest state")
	}
	var events int
	_ = s.DB.QueryRow("SELECT count(*) FROM events").Scan(&events)
	if events != 2 {
		t.Fatalf("duplicate events: %d", events)
	}
	if call("DELETE", "/v1/admin/sessions/"+v.ID, "admin-secret", nil).Code != 200 {
		t.Fatal("delete failed")
	}
	two.Sequence = 3
	_ = call("POST", "/v1/ingest", device.Token, Batch{Events: []Transfer{two}})
	if _, exists := s.Get(v.ID); exists {
		t.Fatal("deleted session resurrected by replay")
	}
	foreign := one
	foreign.Session.MachineID = "another-device"
	if call("POST", "/v1/ingest", device.Token, Batch{Events: []Transfer{foreign}}).Code != 403 {
		t.Fatal("foreign machine accepted")
	}
	_ = call("POST", "/v1/admin/revoke/"+device.DeviceID, "admin-secret", nil)
	if call("GET", "/v1/sessions", device.Token, nil).Code != 401 {
		t.Fatal("revoked credential still works")
	}
}
func TestStateSurvivesRestartAndOfflineDoesNotResolve(t *testing.T) {
	dir := t.TempDir()
	s, e := OpenStore(dir, "Mac")
	if e != nil {
		t.Fatal(e)
	}
	v := testSession(s)
	v.ObservedAt = time.Now().Add(-5 * time.Minute)
	_ = s.Apply(v)
	got, _ := s.Get(v.ID)
	_ = s.Acknowledge(v.ID, got.Attention.ID, "seen")
	s.DB.Close()
	s, e = OpenStore(dir, "Mac")
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	snapshot := s.Snapshot()
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].Connectivity != "offline" || snapshot.Sessions[0].Attention == nil || !snapshot.Sessions[0].Attention.Seen {
		t.Fatal("lost state during restart/offline")
	}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	s, e := OpenStore(t.TempDir(), "test-machine")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.DB.Close() })
	return s
}
func testSession(s *Store) Session {
	now := time.Now().UTC()
	return Session{ID: Key(s.MachineID, "claude", "abc"), NativeID: "abc", MachineID: s.MachineID, Machine: "test-machine", Provider: "claude", Execution: "idle", Outcome: "unknown", Connectivity: "online", StateSince: now, LastActivity: now, ObservedAt: now, RequestKey: "turn-1", RequestKind: "question"}
}
func TestAttentionEpisodesAndStaleUpdates(t *testing.T) {
	s := testStore(t)
	v := testSession(s)
	if e := s.Apply(v); e != nil {
		t.Fatal(e)
	}
	a, _ := s.Get(v.ID)
	id := a.Attention.ID
	if e := s.Acknowledge(v.ID, id, "seen"); e != nil {
		t.Fatal(e)
	}
	v.ObservedAt = v.ObservedAt.Add(time.Second)
	_ = s.Apply(v)
	a, _ = s.Get(v.ID)
	if !a.Attention.Seen {
		t.Fatal("snapshot erased seen state")
	}
	stale := v
	v.RequestKind = ""
	v.Execution = "working"
	v.ObservedAt = v.ObservedAt.Add(time.Second)
	_ = s.Apply(v)
	_ = s.Apply(stale)
	a, _ = s.Get(v.ID)
	if a.Attention != nil {
		t.Fatal("old snapshot resurrected question")
	}
	v.RequestKind = "question"
	v.RequestKey = "turn-2"
	v.ObservedAt = v.ObservedAt.Add(time.Second)
	_ = s.Apply(v)
	a, _ = s.Get(v.ID)
	if a.Attention.ID == id || a.Attention.Seen {
		t.Fatal("new episode inherited old state")
	}
	if s.Acknowledge(v.ID, id, "seen") == nil {
		t.Fatal("accepted stale action")
	}
}
// workingSession applies the test session as working with no request, last active a minute ago.
func workingSession(s *Store) Session {
	v := testSession(s)
	v.RequestKind, v.RequestKey, v.Execution = "", "", "working"
	v.LastActivity = time.Now().Add(-time.Minute)
	_ = s.Apply(v)
	return v
}

// hook applies a Claude hook event for the test session, happening now.
func hook(s *Store, event map[string]any) HookResult {
	event["session_id"] = "abc"
	return ApplyHook(s, "claude", event, time.Now().UTC())
}

// pairStore marks a store as paired with a hub, which enables its outbox.
func pairStore(t *testing.T, s *Store) {
	t.Helper()
	if e := os.WriteFile(filepath.Join(s.DataDir, "remote.json"), []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
}

// testCodexHome creates a home directory with a Codex thread database.
func testCodexHome(t *testing.T) (string, *sql.DB) {
	t.Helper()
	home := t.TempDir()
	if e := os.MkdirAll(filepath.Join(home, ".codex"), 0700); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite", filepath.Join(home, ".codex", "state_5.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	if _, e = db.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT, cwd TEXT, title TEXT, source TEXT, git_branch TEXT, created_at INTEGER, updated_at INTEGER, archived INTEGER, agent_nickname TEXT)`); e != nil {
		t.Fatal(e)
	}
	return home, db
}
func addCodexThread(t *testing.T, db *sql.DB, id, rollout, title, source, nickname string) {
	t.Helper()
	now := time.Now().Unix()
	if _, e := db.Exec(`INSERT INTO threads VALUES(?,?,'/work/project',?,?,'',?,?,0,?)`, id, rollout, title, source, now, now, nickname); e != nil {
		t.Fatal(e)
	}
}

func TestHookRequestSurvivesSnapshotButNotResolution(t *testing.T) {
	s := testStore(t)
	v := workingSession(s)
	hook(s, map[string]any{"hook_event_name": "Notification", "notification_type": "permission_prompt", "message": "Allow this?"})
	a, _ := s.Get(v.ID)
	id := a.Attention.ID
	v.ObservedAt = time.Now()
	_ = s.Apply(v)
	a, _ = s.Get(v.ID)
	if a.Attention == nil || a.Attention.ID != id {
		t.Fatal("poll erased a pending request")
	}
	v.LastActivity = time.Now().Add(time.Second)
	v.ObservedAt = v.LastActivity
	_ = s.Apply(v)
	a, _ = s.Get(v.ID)
	if a.Attention != nil {
		t.Fatal("resolved hook request remained pending")
	}
}
func TestNotificationHookEpisodes(t *testing.T) {
	s := testStore(t)
	v := workingSession(s)
	episode := func(event map[string]any) string {
		t.Helper()
		hook(s, event)
		if a, _ := s.Get(v.ID); a.Attention != nil {
			return a.Attention.ID
		}
		return ""
	}
	ask := func(message string) map[string]any {
		return map[string]any{"hook_event_name": "Notification", "notification_type": "agent_needs_input", "message": message}
	}
	first := episode(ask("Pick a database"))
	if first == "" {
		t.Fatal("notification did not open a request")
	}
	if e := s.Acknowledge(v.ID, first, "seen"); e != nil {
		t.Fatal(e)
	}
	if again := episode(ask("Pick a database")); again != first {
		t.Fatal("re-announced prompt opened a new episode")
	}
	second := episode(ask("Pick a queue"))
	if second == "" || second == first {
		t.Fatal("a distinct question was merged into the previous episode")
	}
	if a, _ := s.Get(v.ID); a.Attention.Seen {
		t.Fatal("new question inherited the previous question's seen state")
	}
	if episode(map[string]any{"hook_event_name": "PostToolUse"}) != "" {
		t.Fatal("activity did not resolve the request")
	}
	if third := episode(ask("Pick a queue")); third == "" || third == second {
		t.Fatal("a prompt after resolution reused the resolved episode")
	}
}
func TestHookForUninventoriedSessionIsReplayed(t *testing.T) {
	s := testStore(t)
	d := &Discovery{Store: s}
	at := time.Now().UTC()
	prompt := map[string]any{"session_id": "abc", "hook_event_name": "PermissionRequest", "tool_use_id": "toolu_1", "tool_name": "Bash", "tool_input": strings.Repeat("x", 1024)}
	result := ApplyHook(s, "claude", prompt, at)
	if result != HookUnknownSession {
		t.Fatal("hook for an unknown session reported as applied")
	}
	d.HookReceived("claude", prompt, result, at)
	d.HookReceived("claude", map[string]any{"session_id": "abc", "hook_event_name": "PostToolUse"}, HookUnknownSession, at)
	if len(d.pending) != 1 || !d.inventoryRequested.Load() {
		t.Fatalf("expected only the prompt queued with an inventory requested; queued %d", len(d.pending))
	}
	if _, kept := d.pending[0].event["tool_input"]; kept {
		t.Fatal("queued hook kept a payload field ApplyHook doesn't read")
	}
	d.replayHooks()
	if len(d.pending) != 1 {
		t.Fatal("hook was dropped before its session appeared")
	}
	v := workingSession(s)
	d.replayHooks()
	a, _ := s.Get(v.ID)
	if len(d.pending) != 0 || a.Attention == nil || a.Attention.Kind != "permission" || !a.HookAt.Equal(at) {
		t.Fatal("queued permission prompt was not applied at its original time once the session appeared")
	}
}
func TestLaterWorkingInventoryResolvesPermissionPrompt(t *testing.T) {
	for _, tc := range []struct {
		name         string
		inventoryAge time.Duration // inventory time relative to the hook
		wantPending  bool
	}{
		{"inventory too soon after the prompt", time.Second, true},
		{"inventory well after the prompt", 10 * time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t)
			v := workingSession(s)
			hook(s, map[string]any{"hook_event_name": "PermissionRequest", "tool_use_id": "toolu_1", "tool_name": "Bash"})
			prompted, _ := s.Get(v.ID)
			// A future lastInventory keeps claude() from running the real CLI.
			d := &Discovery{Store: s, Home: t.TempDir(), lastInventory: prompted.HookAt.Add(tc.inventoryAge)}
			d.claudeInventory = []map[string]any{{"sessionId": "abc", "cwd": "/work", "name": "Test", "kind": "background", "state": "working"}}
			d.Refresh(context.Background())
			got, _ := s.Get(v.ID)
			if pending := got.Attention != nil && got.Attention.Kind == "permission"; pending != tc.wantPending {
				t.Fatalf("permission pending = %v, want %v", pending, tc.wantPending)
			}
		})
	}
}
func TestSessionStartDoesNotStartWork(t *testing.T) {
	s := testStore(t)
	v := testSession(s)
	v.RequestKind, v.RequestKey = "", ""
	_ = s.Apply(v)
	if hook(s, map[string]any{"hook_event_name": "SessionStart", "source": "compact"}) == HookUnknownSession {
		t.Fatal("known session reported as unknown")
	}
	if got, _ := s.Get(v.ID); got.Execution != "idle" {
		t.Fatalf("SessionStart changed execution to %s", got.Execution)
	}
}
func TestOnlyLifecycleHooksRequestInventory(t *testing.T) {
	s := testStore(t)
	workingSession(s)
	d := &Discovery{Store: s}
	receive := func(provider string, event map[string]any) {
		event["session_id"] = "abc"
		at := time.Now().UTC()
		d.HookReceived(provider, event, ApplyHook(s, provider, event, at), at)
	}
	receive("claude", map[string]any{"hook_event_name": "PostToolUse"})
	receive("claude", map[string]any{"hook_event_name": "UserPromptSubmit"})
	if d.inventoryRequested.Load() || d.codexDirty.Load() {
		t.Fatal("tool and prompt hooks forced an inventory or Codex query")
	}
	receive("claude", map[string]any{"hook_event_name": "Notification", "notification_type": "permission_prompt", "message": "Allow?"})
	if !d.inventoryRequested.Load() || d.codexDirty.Load() {
		t.Fatal("a prompt notification did not request inventory, or dirtied Codex")
	}
	receive("codex", map[string]any{"type": "agent-turn-complete"})
	if !d.codexDirty.Load() {
		t.Fatal("a Codex hook did not refresh Codex")
	}
}
func TestAbandonedCodexTurnStopsWorking(t *testing.T) {
	home, db := testCodexHome(t)
	hourAgo := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	started := `{"timestamp":"` + hourAgo + `","type":"event_msg","payload":{"type":"task_started"}}` + "\n"
	for id, body := range map[string]string{
		"dead":    started,
		"testing": started + `{"timestamp":"` + hourAgo + `","type":"response_item","payload":{"type":"function_call","call_id":"c1","name":"shell"}}` + "\n",
	} {
		path := filepath.Join(home, id+".jsonl")
		if e := os.WriteFile(path, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
		addCodexThread(t, db, id, path, id, "cli", "")
	}
	s := testStore(t)
	d := &Discovery{Store: s, Home: home}
	d.codex(context.Background())
	defer d.codexDB.Close()
	dead, _ := s.Get(Key(s.MachineID, "codex", "dead"))
	running, _ := s.Get(Key(s.MachineID, "codex", "testing"))
	if dead.Execution == "working" || dead.Connectivity != "online" {
		t.Fatalf("a Codex turn silent for an hour still counts as working: %s/%s", dead.Execution, dead.Connectivity)
	}
	if running.Execution != "working" {
		t.Fatal("a Codex turn with a running tool call was ended too soon")
	}
}
func TestMissingProvidersAreNotMissingSources(t *testing.T) {
	s := testStore(t)
	d := &Discovery{Store: s, Home: t.TempDir(), Claude: filepath.Join(t.TempDir(), "claude")}
	d.Refresh(context.Background())
	for _, h := range s.Snapshot().Sources {
		if (h.ID == "claude" || h.ID == "codex") && h.Status != "unsupported" {
			t.Fatalf("%s is %s when not installed", h.ID, h.Status)
		}
	}
}
func TestHistoryGapWarningClears(t *testing.T) {
	s := testStore(t)
	pairStore(t, s)
	_, _ = s.DB.Exec("INSERT INTO events(at,session_id,body) VALUES(?,?,?)", time.Now().Add(-8*24*time.Hour).Unix(), "old", []byte("old"))
	_ = s.Prune()
	hasGap := func() bool {
		for _, h := range s.Snapshot().Sources {
			if h.ID == "outbox" {
				return true
			}
		}
		return false
	}
	if !hasGap() {
		t.Fatal("history gap was not reported")
	}
	_ = s.Prune()
	if !hasGap() {
		t.Fatal("history gap warning disappeared immediately")
	}
	s.mu.Lock()
	h := s.health["outbox"]
	h.UpdatedAt = time.Now().Add(-2 * time.Hour)
	s.health["outbox"] = h
	s.mu.Unlock()
	_ = s.Prune()
	if hasGap() {
		t.Fatal("history gap warning never cleared")
	}
}
func TestOutboxKeepsOnlyLatestPerSession(t *testing.T) {
	s := testStore(t)
	for _, v := range []struct{ session, body string }{{"a", "a1"}, {"b", "b1"}, {"a", "a2"}, {"a", "a3"}} {
		_, _ = s.DB.Exec("INSERT INTO events(at,session_id,body) VALUES(?,?,?)", time.Now().Unix(), v.session, []byte(v.body))
	}
	_, _ = s.DB.Exec("INSERT INTO events(at,session_id,body,sent) VALUES(?,?,?,1)", time.Now().Unix(), "a", []byte("sent"))
	if e := s.CoalesceOutbox(); e != nil {
		t.Fatal(e)
	}
	rows, _ := s.DB.Query("SELECT body FROM events WHERE sent=0 ORDER BY seq")
	var bodies []string
	for rows.Next() {
		var b []byte
		_ = rows.Scan(&b)
		bodies = append(bodies, string(b))
	}
	rows.Close()
	if strings.Join(bodies, ",") != "b1,a3" {
		t.Fatalf("outbox should hold only the newest event per session, got %v", bodies)
	}
}
func TestVanishedClaudeSessionEnds(t *testing.T) {
	s := testStore(t)
	gone := workingSession(s)
	hook(s, map[string]any{"hook_event_name": "Notification", "notification_type": "permission_prompt", "message": "Allow Bash?"})
	// A stand-in for the Claude CLI whose inventory no longer lists the session.
	cli := filepath.Join(t.TempDir(), "claude")
	inventory := `[{"sessionId":"still-listed","cwd":"/work","name":"other","kind":"background","state":"working"}]`
	if e := os.WriteFile(cli, []byte("#!/bin/sh\necho '"+inventory+"'\n"), 0700); e != nil {
		t.Fatal(e)
	}
	d := &Discovery{Store: s, Home: t.TempDir(), Claude: cli}
	d.Refresh(context.Background())
	v, _ := s.Get(gone.ID)
	if v.Execution != "ended" || v.Attention != nil {
		t.Fatalf("vanished session kept its state: execution=%s attention=%v", v.Execution, v.Attention)
	}
	if _, ok := s.Get(Key(s.MachineID, "claude", "still-listed")); !ok {
		t.Fatal("listed session was not applied")
	}
}
func TestCodexInventoryIsThrottledUnlessDirty(t *testing.T) {
	home, db := testCodexHome(t)
	addCodexThread(t, db, "parent", filepath.Join(home, "parent.jsonl"), "Parent thread", "cli", "")
	s := testStore(t)
	d := &Discovery{Store: s, Home: home}
	ctx := context.Background()
	d.codex(ctx)
	defer d.codexDB.Close()
	addCodexThread(t, db, "child", filepath.Join(home, "child.jsonl"), "", `{"subagent":{"thread_spawn":{"parent_thread_id":"parent"}}}`, "Feynman")
	d.codex(ctx)
	if _, ok := s.Get(Key(s.MachineID, "codex", "child")); ok {
		t.Fatal("Codex database was re-queried inside the reconciliation interval")
	}
	d.codexDirty.Store(true)
	d.codex(ctx)
	child, ok := s.Get(Key(s.MachineID, "codex", "child"))
	if !ok {
		t.Fatal("a relevant Codex change did not force a refresh")
	}
	if child.Title != "Feynman" || child.ParentID != Key(s.MachineID, "codex", "parent") {
		t.Fatalf("subagent title or parent missing: %q %q", child.Title, child.ParentID)
	}
}
func TestNoteFileEvent(t *testing.T) {
	home := t.TempDir()
	for name, want := range map[string]bool{
		filepath.Join(home, ".codex", "sessions", "2026", "09", "10", "rollout.jsonl"): true,
		filepath.Join(home, ".codex", "state_5.sqlite-wal"):                          true,
		filepath.Join(home, ".codex", "logs_2.sqlite-wal"):                           false,
		filepath.Join(home, ".codex", "history.jsonl"):                               false,
		filepath.Join(home, ".claude", "projects", "p", "session.jsonl"):             true,
	} {
		d := &Discovery{Home: home}
		if got := d.NoteFileEvent(name); got != want {
			t.Fatalf("NoteFileEvent(%s) = %v, want %v", name, got, want)
		}
		if codex := strings.Contains(name, ".codex"); d.codexDirty.Load() != (codex && want) {
			t.Fatalf("NoteFileEvent(%s) set Codex dirty = %v", name, d.codexDirty.Load())
		}
	}
}
func TestUnpairedCollectorKeepsNoOutbox(t *testing.T) {
	s := testStore(t)
	v := testSession(s)
	_ = s.Apply(v)
	first := s.Snapshot().Sequence
	v.Summary = "changed"
	v.ObservedAt = v.ObservedAt.Add(time.Second)
	_ = s.Apply(v)
	if s.Snapshot().Sequence <= first {
		t.Fatal("snapshot sequence did not advance without an outbox")
	}
	var n int
	_ = s.DB.QueryRow("SELECT count(*) FROM events").Scan(&n)
	if n != 0 {
		t.Fatalf("unpaired collector queued %d events", n)
	}
}
func TestTailPartialRecordRotationAndBookkeeping(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session.jsonl")
	date := "2026-09-10T12:00:00Z"
	line := `{"type":"assistant","timestamp":"` + date + `","message":{"id":"m1","content":[{"type":"text","text":"Question?"}],"usage":{"output_tokens":20}}}`
	_ = os.WriteFile(p, []byte(line[:20]), 0600)
	tail := newTail()
	_, _ = tail.Read(p, "claude")
	if tail.Offset != 0 {
		t.Fatal("consumed incomplete record")
	}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	_, _ = f.WriteString(line[20:] + "\n" + line + "\n" + `{"type":"system","timestamp":"2026-09-11T00:00:00Z"}` + "\n")
	f.Close()
	_, _ = tail.Read(p, "claude")
	if tail.LastTurn != timestamp(date) {
		t.Fatal("bookkeeping moved state time")
	}
	if tail.LastActivity != timestamp(date) {
		t.Fatal("bookkeeping counted as activity")
	}
	_ = os.Remove(p)
	_ = os.WriteFile(p, []byte(`{"type":"user","timestamp":"2026-09-10T13:00:00Z","message":{"content":"continue"}}`+"\n"), 0600)
	_, _ = tail.Read(p, "claude")
	if tail.Execution != "working" {
		t.Fatal("failed to reset on replacement")
	}
}
func TestClaudeInterruptStopsTurn(t *testing.T) {
	for _, text := range []string{"[Request interrupted by user]", "[Request interrupted by user for tool use]"} {
		p := filepath.Join(t.TempDir(), "session.jsonl")
		lines := `{"type":"user","timestamp":"2026-09-10T12:00:00Z","message":{"content":"run the tests"}}` + "\n" +
			`{"type":"assistant","timestamp":"2026-09-10T12:00:01Z","message":{"id":"m1","content":[{"type":"tool_use","id":"t1","name":"Bash"}]}}` + "\n" +
			`{"type":"user","timestamp":"2026-09-10T12:00:05Z","message":{"content":[{"type":"text","text":"` + text + `"}]}}` + "\n"
		_ = os.WriteFile(p, []byte(lines), 0600)
		tail := newTail()
		if _, e := tail.Read(p, "claude"); e != nil {
			t.Fatal(e)
		}
		if _, open := tail.Buckets(timestamp("2026-09-10T12:00:10Z")); tail.Execution != "idle" || tail.Outcome != "interrupted" || open != 0 {
			t.Fatalf("%q left the turn %s/%s with %d open tools", text, tail.Execution, tail.Outcome, open)
		}
	}
}
func TestUsageDedupAndCodexCounterReset(t *testing.T) {
	tail := newTail()
	now := time.Now().UTC()
	parse := func(v map[string]any, p string) { v["timestamp"] = now.Format(time.RFC3339Nano); tail.Parse(v, p) }
	parse(map[string]any{"type": "assistant", "message": map[string]any{"id": "same", "usage": map[string]any{"output_tokens": float64(12)}}}, "claude")
	parse(map[string]any{"type": "assistant", "message": map[string]any{"id": "same", "usage": map[string]any{"output_tokens": float64(15)}}}, "claude")
	b, _ := tail.Buckets(now)
	n := 0
	for _, v := range b {
		n += v.Tokens
	}
	if n != 15 {
		t.Fatalf("double counted content blocks: %d", n)
	}
	tail = newTail()
	for i, total := range []int{100, 125, 10, 13} {
		now = now.Add(time.Second)
		parse(map[string]any{"type": "event_msg", "payload": map[string]any{"type": "token_count", "info": map[string]any{"total_token_usage": map[string]any{"output_tokens": float64(total)}}}}, "codex")
		_ = i
	}
	b, _ = tail.Buckets(now)
	n = 0
	for _, v := range b {
		n += v.Tokens
	}
	if n != 28 {
		t.Fatalf("bad reset handling: %d", n)
	}
}
func TestClaudeSuccessfulRetryClearsFailure(t *testing.T) {
	tail := newTail()
	var event map[string]any
	_ = json.Unmarshal([]byte(`{"type":"assistant","timestamp":"2026-09-10T12:00:00Z","uuid":"error","isApiErrorMessage":true,"message":{"content":"API failure"}}`), &event)
	tail.Parse(event, "claude")
	if tail.RequestKind != "failure" {
		t.Fatal("missing failure")
	}
	event = map[string]any{}
	_ = json.Unmarshal([]byte(`{"type":"assistant","timestamp":"2026-09-10T12:00:01Z","message":{"content":"Recovered output"}}`), &event)
	tail.Parse(event, "claude")
	if tail.RequestKind != "" || tail.Outcome != "completed" {
		t.Fatal("successful retry retained a failure")
	}
}
func TestPublicSessionIsAllowlisted(t *testing.T) {
	s := testSession(testStore(t))
	s.Title = "secret prompt"
	s.Summary = "secret output"
	s.Cwd = "/secret"
	s.Transcript = "/secret/log"
	s.Branch = "secret branch"
	s.AttachID = "secret command"
	s.Aliases = []string{"secret alias"}
	s.RequestKey = "secret request"
	b := string(wireJSON(PublicSession(s)))
	if strings.Contains(b, "secret") {
		t.Fatal(b)
	}
}
func TestHookInstallPreservesAndRestoresNotify(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".claude"), 0700)
	_ = os.MkdirAll(filepath.Join(home, ".codex"), 0700)
	claude := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"existing-hook"}]}]},"other":true}`
	codex := "# Keep this comment\nnotify = [\n  \"original-handler\", \"literal $HOME and spaces\"\n]\nmodel = \"test-model\"\n[features]\nexample=true\n"
	_ = os.WriteFile(filepath.Join(home, ".claude/settings.json"), []byte(claude), 0600)
	_ = os.WriteFile(filepath.Join(home, ".codex/config.toml"), []byte(codex), 0600)
	for i := 0; i < 2; i++ {
		if e := InstallHooks(home, dir, "/some path/collector", false); e != nil {
			t.Fatal(e)
		}
	}
	b, _ := os.ReadFile(filepath.Join(home, ".claude/settings.json"))
	if !strings.Contains(string(b), "existing-hook") || !strings.Contains(string(b), `"other": true`) {
		t.Fatal("lost existing config")
	}
	if e := InstallHooks(home, dir, "/some path/collector", true); e != nil {
		t.Fatal(e)
	}
	b, _ = os.ReadFile(filepath.Join(home, ".codex/config.toml"))
	if !strings.Contains(string(b), "# Keep this comment") || !strings.Contains(string(b), "original-handler") || !strings.Contains(string(b), "literal $HOME and spaces") {
		t.Fatal(string(b))
	}
	b, _ = os.ReadFile(filepath.Join(home, ".claude/settings.json"))
	if strings.Contains(string(b), "/some path/") {
		t.Fatal("owned hook survived removal")
	}
}
func TestHookReinstallAfterMoveAndRetryAfterFailure(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	_ = os.MkdirAll(claudeDir, 0700)
	_ = os.MkdirAll(filepath.Join(home, ".codex"), 0700)
	_ = os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{"hooks":{}}`), 0600)
	_ = os.WriteFile(filepath.Join(home, ".codex/config.toml"), []byte("notify = [\"original-handler\"]\n"), 0600)
	// A failed installation leaves no backup behind, so a retry isn't refused.
	_ = os.Chmod(claudeDir, 0500)
	if InstallHooks(home, dir, "/old place/collector", false) == nil {
		t.Fatal("installation succeeded without a writable Claude settings directory")
	}
	_ = os.Chmod(claudeDir, 0700)
	if _, e := os.Stat(filepath.Join(dir, "notify-backup.json")); !os.IsNotExist(e) {
		t.Fatal("failed installation left a Codex notify backup")
	}
	if e := InstallHooks(home, dir, "/old place/collector", false); e != nil {
		t.Fatalf("retry after failure: %v", e)
	}
	// Reinstalling from a new location replaces, rather than duplicates, the owned entries.
	if e := InstallHooks(home, dir, "/new place/collector", false); e != nil {
		t.Fatalf("reinstall after move: %v", e)
	}
	settings, _ := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	codex, _ := os.ReadFile(filepath.Join(home, ".codex/config.toml"))
	if strings.Contains(string(settings), "/old place/") || strings.Count(string(settings), "/new place/collector") != 7 {
		t.Fatalf("Claude hooks not replaced after move: %s", settings)
	}
	if strings.Contains(string(codex), "/old place/") || !strings.Contains(string(codex), "/new place/collector") {
		t.Fatalf("Codex wrapper not updated after move: %s", codex)
	}
	if e := InstallHooks(home, dir, "/new place/collector", true); e != nil {
		t.Fatal(e)
	}
	settings, _ = os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	codex, _ = os.ReadFile(filepath.Join(home, ".codex/config.toml"))
	if strings.Contains(string(settings), "collector") || !strings.Contains(string(codex), "original-handler") || strings.Contains(string(codex), "collector") {
		t.Fatalf("removal incomplete: %s / %s", settings, codex)
	}
}
func TestFeedbackRecordsProjectionLocally(t *testing.T) {
	s := testStore(t)
	h := Authorized("test-token", LocalAPI(s, nil))
	v := testSession(s)
	v.Summary = "Which database?"
	_ = s.Apply(v)
	post := func(body map[string]string, want int) {
		t.Helper()
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/v1/feedback", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer test-token")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("got %d (%s), want %d", w.Code, w.Body.String(), want)
		}
	}
	post(map[string]string{"sessionID": v.ID, "verdict": "not_waiting", "note": "answered in terminal"}, http.StatusOK)
	post(map[string]string{"sessionID": "missing", "verdict": "not_waiting"}, http.StatusNotFound)
	post(map[string]string{"sessionID": v.ID, "verdict": "maybe"}, http.StatusBadRequest)
	path := filepath.Join(s.DataDir, "feedback.jsonl")
	info, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("feedback file mode %v", info.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	var entry FeedbackEntry
	if lines := strings.Split(strings.TrimSpace(string(b)), "\n"); len(lines) != 1 || json.Unmarshal([]byte(lines[0]), &entry) != nil {
		t.Fatalf("expected one feedback line: %q", b)
	}
	if entry.Verdict != "not_waiting" || entry.Session.ID != v.ID || entry.Session.Attention == nil || entry.Session.Summary != "Which database?" {
		t.Fatalf("feedback did not capture the session projection: %+v", entry)
	}
	var report bytes.Buffer
	if e = FeedbackReport(s.DataDir, &report); e != nil || !strings.Contains(report.String(), "1  not_waiting · claude") {
		t.Fatalf("report: %v %q", e, report.String())
	}
}
func TestAPIRequiresTokenAndCurrentEpisode(t *testing.T) {
	s := testStore(t)
	h := Authorized("test-token", LocalAPI(s, nil))
	for _, token := range []string{"", "wrong"} {
		req := httptest.NewRequest("GET", "/v1/sessions", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatal(w.Code)
		}
	}
	req := httptest.NewRequest("GET", "/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var v Snapshot
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatal(w.Body.String())
	}
	entry := testSession(s)
	if err := s.Apply(entry); err != nil {
		t.Fatal(err)
	}
	old, _ := s.Get(entry.ID)
	entry.RequestKey = "turn-2"
	entry.ObservedAt = entry.ObservedAt.Add(time.Second)
	if err := s.Apply(entry); err != nil {
		t.Fatal(err)
	}
	current, _ := s.Get(entry.ID)
	post := func(episode, action string, want int) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"sessionID": entry.ID, "episodeID": episode, "action": action})
		req := httptest.NewRequest("POST", "/v1/attention", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-token")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s: got %d (%s), want %d", action, w.Code, w.Body.String(), want)
		}
	}
	post(old.Attention.ID, "seen", http.StatusConflict)
	untouched, _ := s.Get(entry.ID)
	if untouched.Attention.Seen {
		t.Fatal("stale acknowledgment affected the new request")
	}
	post(current.Attention.ID, "invalid", http.StatusBadRequest)
	post(current.Attention.ID, "seen", http.StatusOK)
	seen, _ := s.Get(entry.ID)
	if !seen.Attention.Seen {
		t.Fatal("current acknowledgment was not saved")
	}
	if err := s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	post(current.Attention.ID, "notified", http.StatusInternalServerError)
	uncommitted, _ := s.Get(entry.ID)
	if uncommitted.Attention.Notified {
		t.Fatal("failed write changed in-memory acknowledgment")
	}
}

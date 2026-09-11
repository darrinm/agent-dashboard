package core

import (
	"bytes"
	"context"
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
func TestHookRequestSurvivesSnapshotButNotResolution(t *testing.T) {
	s := testStore(t)
	v := testSession(s)
	v.RequestKind = ""
	v.Execution = "working"
	v.LastActivity = time.Now().Add(-time.Minute)
	_ = s.Apply(v)
	ApplyHook(s, "claude", map[string]any{"session_id": "abc", "hook_event_name": "Notification", "notification_type": "permission_prompt", "message": "Allow this?"})
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

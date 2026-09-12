package core

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
)

type Usage struct {
	At    time.Time `json:"at"`
	Count int       `json:"count"`
}
type Span struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}
type Tail struct {
	Offset         int64            `json:"offset"`
	Inode          uint64           `json:"inode"`
	Execution      string           `json:"execution"`
	Outcome        string           `json:"outcome"`
	RequestKind    string           `json:"requestKind"`
	RequestKey     string           `json:"requestKey"`
	LastActivity   time.Time        `json:"lastActivity"`
	LastTurn       time.Time        `json:"lastTurn"`
	LastUser       time.Time        `json:"lastUser"`
	Summary        string           `json:"summary"`
	Branch         string           `json:"branch"`
	Alias          string           `json:"alias"`
	Usage          map[string]Usage `json:"usage"`
	Tools          map[string]Span  `json:"tools"`
	Total          int              `json:"total"`
	HasTotal       bool             `json:"hasTotal"`
	HasActivity    bool             `json:"hasActivity"`
	PartialHistory bool             `json:"partialHistory"`
	SkipLine       bool             `json:"skipLine"`
}

func newTail() *Tail {
	return &Tail{Execution: "unknown", Outcome: "unknown", Usage: map[string]Usage{}, Tools: map[string]Span{}}
}
func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func obj(m map[string]any, k string) map[string]any {
	x, _ := m[k].(map[string]any)
	if x == nil {
		return map[string]any{}
	}
	return x
}
func num(m map[string]any, k string) int { v, _ := m[k].(float64); return int(v) }
func timestamp(s string) time.Time       { t, _ := time.Parse(time.RFC3339Nano, s); return t.UTC() }
func (t *Tail) activity(at time.Time) {
	if at.After(t.LastActivity) {
		t.LastActivity = at
	}
}
func (t *Tail) clearRequest() { t.RequestKind = ""; t.RequestKey = "" }
func (t *Tail) setRequest(kind, key string, at time.Time) {
	t.RequestKind = kind
	t.RequestKey = key
	t.LastTurn = at
}
func (t *Tail) Read(path, provider string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	ino := info.Sys().(*syscall.Stat_t).Ino
	if t.Inode != 0 && (t.Inode != ino || info.Size() < t.Offset) {
		*t = *newTail()
	}
	t.Inode = ino
	if t.Offset == 0 && info.Size() > 512*1024 {
		t.Offset = info.Size() - 512*1024
		t.PartialHistory = true
		_, _ = f.Seek(t.Offset, io.SeekStart)
		r := bufio.NewReader(f)
		b, _ := r.ReadBytes('\n')
		t.Offset += int64(len(b))
	}
	if info.Size() == t.Offset {
		return false, nil
	}
	_, err = f.Seek(t.Offset, io.SeekStart)
	if err != nil {
		return false, err
	}
	r := bufio.NewReader(io.LimitReader(f, 4*1024*1024))
	changed := false
	for {
		line, e := r.ReadBytes('\n')
		if e != nil {
			// Advance over oversized records without retaining their payload. A
			// normal incomplete final record stays at its start until completed.
			if len(line) >= 4*1024*1024 || t.SkipLine {
				t.Offset += int64(len(line))
				t.SkipLine = true
				t.PartialHistory = true
				changed = true
			}
			break
		}
		t.Offset += int64(len(line))
		changed = true
		if t.SkipLine {
			t.SkipLine = false
			continue
		}
		var v map[string]any
		if json.Unmarshal(line, &v) != nil {
			continue
		}
		t.Parse(v, provider)
		changed = true
	}
	cut := time.Now().Add(-16 * time.Minute)
	for id, u := range t.Usage {
		if u.At.Before(cut) {
			delete(t.Usage, id)
		}
	}
	for id, span := range t.Tools {
		if !span.End.IsZero() && span.End.Before(cut) {
			delete(t.Tools, id)
		}
	}
	return changed, nil
}
func (t *Tail) Parse(d map[string]any, provider string) {
	if t.Usage == nil {
		t.Usage = map[string]Usage{}
	}
	if t.Tools == nil {
		t.Tools = map[string]Span{}
	}
	at := timestamp(str(d, "timestamp"))
	typ := str(d, "type")
	if provider == "claude" {
		if b := str(d, "gitBranch"); b != "" {
			t.Branch = b
		}
		if typ == "bridge-session" {
			t.Alias = str(d, "bridgeSessionId")
		}
		m := obj(d, "message")
		content, _ := m["content"].([]any)
		if typ == "system" && str(d, "subtype") == "model_refusal_no_fallback" && !at.IsZero() {
			t.Execution = "idle"
			t.Outcome = "failed"
			t.setRequest("failure", str(d, "uuid"), at)
			t.Summary = "The model refused and no fallback was available"
			t.activity(at)
			return
		}
		if typ != "assistant" && typ != "user" {
			return
		}
		if at.IsZero() {
			return
		}
		isToolResult := false
		hasTool := false
		var texts []string
		for _, c := range content {
			v, _ := c.(map[string]any)
			switch str(v, "type") {
			case "text":
				texts = append(texts, str(v, "text"))
			case "tool_use":
				hasTool = true
				t.Tools[str(v, "id")] = Span{Start: at}
				name := str(v, "name")
				if name == "AskUserQuestion" {
					t.setRequest("question", str(v, "id"), at)
					input := obj(v, "input")
					t.Summary = Clip(string(wireJSON(input["questions"])), 600)
				}
			case "tool_result":
				isToolResult = true
				id := str(v, "tool_use_id")
				span := t.Tools[id]
				span.End = at
				t.Tools[id] = span
				if t.RequestKey == id {
					t.clearRequest()
				}
			}
		}
		if text, ok := m["content"].(string); ok {
			texts = append(texts, text)
		}
		if typ == "user" && !isToolResult {
			t.LastUser = at
			t.LastTurn = at
			t.clearRequest()
			if interruptedByUser(texts) {
				// Claude records an interrupt as a user text line; the turn has stopped.
				t.closeTools(at)
				t.Execution = "idle"
				t.Outcome = "interrupted"
			} else {
				t.Execution = "working"
				t.Outcome = "unknown"
			}
		}
		if typ == "assistant" {
			t.LastTurn = at
			failed, _ := d["isApiErrorMessage"].(bool)
			if !failed && t.RequestKind == "failure" {
				t.clearRequest()
			}
			if len(texts) > 0 {
				t.Summary = Clip(strings.Join(texts, "\n"), 600)
			}
			if failed {
				t.Execution = "idle"
				t.Outcome = "failed"
				t.setRequest("failure", str(d, "uuid"), at)
			} else if hasTool {
				t.Execution = "working"
				t.Outcome = "unknown"
			} else if len(texts) > 0 {
				t.Execution = "idle"
				t.Outcome = "completed"
			}
			usage := obj(m, "usage")
			if _, ok := usage["output_tokens"]; ok {
				t.HasActivity = true
				id := str(m, "id")
				if id != "" {
					u := t.Usage[id]
					count := num(usage, "output_tokens")
					if count > u.Count {
						t.Usage[id] = Usage{at, count}
					}
				}
			}
		}
		t.activity(at)
		return
	}
	p := obj(d, "payload")
	pt := str(p, "type")
	if typ == "event_msg" {
		switch pt {
		case "task_started":
			t.Execution = "working"
			t.Outcome = "unknown"
			t.LastTurn = at
			t.LastUser = at
			t.clearRequest()
			t.activity(at)
		case "task_complete":
			t.closeTools(at)
			t.Execution = "idle"
			t.Outcome = "completed"
			t.LastTurn = at
			t.clearRequest()
			if msg := str(p, "last_agent_message"); msg != "" {
				t.Summary = Clip(msg, 600)
			}
			t.activity(at)
		case "turn_aborted":
			t.closeTools(at)
			t.Execution = "idle"
			t.Outcome = "interrupted"
			t.LastTurn = at
			t.clearRequest()
			t.activity(at)
		case "token_count":
			info := obj(p, "info")
			usage := obj(info, "total_token_usage")
			if _, ok := usage["output_tokens"]; ok {
				total := num(usage, "output_tokens")
				if t.HasTotal && total >= t.Total {
					delta := total - t.Total
					if delta > 0 {
						t.Usage[at.Format(time.RFC3339Nano)] = Usage{at, delta}
					}
				}
				t.Total = total
				t.HasTotal = true
				t.HasActivity = true
			}
		case "agent_message":
			if msg := str(p, "message"); msg != "" {
				t.Summary = Clip(msg, 600)
			}
			t.activity(at)
		}
		return
	}
	if typ != "response_item" || at.IsZero() {
		return
	}
	switch pt {
	case "function_call", "custom_tool_call":
		id := str(p, "call_id")
		t.Tools[id] = Span{Start: at}
		t.Execution = "working"
		name := str(p, "name")
		if strings.Contains(name, "request_user_input") && !strings.Contains(name, "async") {
			t.setRequest("question", id, at)
			t.Summary = "Has a question for you"
		}
		t.activity(at)
	case "function_call_output", "custom_tool_call_output":
		id := str(p, "call_id")
		span := t.Tools[id]
		span.End = at
		t.Tools[id] = span
		if t.RequestKey == id {
			t.clearRequest()
		}
		t.activity(at)
	case "message":
		content, _ := p["content"].([]any)
		var texts []string
		for _, c := range content {
			v, _ := c.(map[string]any)
			if text := str(v, "text"); text != "" {
				texts = append(texts, text)
			}
		}
		if str(p, "role") == "assistant" && len(texts) > 0 {
			t.Summary = Clip(strings.Join(texts, "\n"), 600)
			t.LastTurn = at
		}
		t.activity(at)
	}
}
func (t *Tail) closeTools(at time.Time) {
	for id, span := range t.Tools {
		if span.End.IsZero() {
			span.End = at
			t.Tools[id] = span
		}
	}
}
func interruptedByUser(texts []string) bool {
	for _, text := range texts {
		if strings.HasPrefix(strings.TrimSpace(text), "[Request interrupted by user") {
			return true
		}
	}
	return false
}
func (t *Tail) Buckets(now time.Time) ([]Bucket, int) {
	start := now.UTC().Truncate(30 * time.Second).Add(-29 * 30 * time.Second)
	out := make([]Bucket, 30)
	for i := range out {
		out[i].At = start.Add(time.Duration(i) * 30 * time.Second)
	}
	for _, u := range t.Usage {
		i := int(u.At.Sub(start) / (30 * time.Second))
		if !u.At.Before(start) && i >= 0 && i < 30 {
			out[i].Tokens += u.Count
		}
	}
	open := 0
	for _, s := range t.Tools {
		end := s.End
		if end.IsZero() {
			end = now
			open++
		}
		if s.Start.IsZero() {
			continue
		}
		for i, b := range out {
			if s.Start.Before(b.At.Add(30*time.Second)) && end.After(b.At) {
				out[i].Tool = true
			}
		}
	}
	return out, open
}

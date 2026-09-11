package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const Version = "0.1.0"

type Episode struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	OpenedAt     time.Time `json:"openedAt"`
	Seen         bool      `json:"seen"`
	Notified     bool      `json:"notified"`
	SnoozedUntil time.Time `json:"snoozedUntil"`
}
type Bucket struct {
	At     time.Time `json:"at"`
	Tokens int       `json:"tokens"`
	Tool   bool      `json:"tool"`
}
type Session struct {
	ID           string    `json:"id"`
	NativeID     string    `json:"nativeID"`
	MachineID    string    `json:"machineID"`
	Machine      string    `json:"machine"`
	Provider     string    `json:"provider"`
	Source       string    `json:"source"`
	Kind         string    `json:"kind"`
	Title        string    `json:"title"`
	Project      string    `json:"project"`
	Cwd          string    `json:"cwd,omitempty"`
	Branch       string    `json:"branch,omitempty"`
	ParentID     string    `json:"parentID,omitempty"`
	Aliases      []string  `json:"aliases"`
	Execution    string    `json:"execution"`
	Outcome      string    `json:"outcome"`
	Connectivity string    `json:"connectivity"`
	Evidence     string    `json:"evidence"`
	Attention    *Episode  `json:"attention,omitempty"`
	LastActivity time.Time `json:"lastActivity"`
	StateSince   time.Time `json:"stateSince"`
	ObservedAt   time.Time `json:"observedAt"`
	Summary      string    `json:"summary"`
	Capabilities []string  `json:"capabilities"`
	Transcript   string    `json:"transcript,omitempty"`
	AttachID     string    `json:"attachID,omitempty"`
	Buckets      []Bucket  `json:"buckets"`
	HasActivity  bool      `json:"hasActivity"`
	OpenTools    int       `json:"openTools"`
	Remote       bool      `json:"remote"`
	RequestKey   string    `json:"requestKey,omitempty"`
	RequestKind  string    `json:"requestKind,omitempty"`
	HookAt       time.Time `json:"hookAt"`
}
type Health struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Detail    string    `json:"detail"`
	UpdatedAt time.Time `json:"updatedAt"`
}
type Snapshot struct {
	Version   string    `json:"version"`
	Sequence  int64     `json:"sequence"`
	Sessions  []Session `json:"sessions"`
	Sources   []Health  `json:"sources"`
	MachineID string    `json:"machineID"`
	Machine   string    `json:"machine"`
}

func ID() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func Hash(s string) string                        { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func Key(machine, provider, native string) string { return machine + ":" + provider + ":" + native }
func Clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		r = append(r[:n], '…')
	}
	return string(r)
}
func Project(cwd string) string {
	if cwd == "" {
		return "No project"
	}
	return filepath.Base(cwd)
}
func IsAttention(e *Episode) bool { return e != nil && e.Kind != "review" }
func SortSessions(s []Session) {
	sort.SliceStable(s, func(i, j int) bool {
		a, b := s[i], s[j]
		rank := func(s Session) int {
			if IsAttention(s.Attention) {
				return 0
			}
			if s.Execution == "working" {
				return 1
			}
			if s.Attention != nil {
				return 2
			}
			return 3
		}
		if rank(a) != rank(b) {
			return rank(a) < rank(b)
		}
		if a.Attention != nil && b.Attention != nil {
			return a.Attention.OpenedAt.After(b.Attention.OpenedAt)
		}
		return a.LastActivity.After(b.LastActivity)
	})
}
func wireJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// Remote transport is an allowlist, never a serialization of the local record.
func PublicSession(s Session) Session {
	o := Session{ID: s.ID, NativeID: s.NativeID, MachineID: s.MachineID, Machine: s.Machine, Provider: s.Provider, Source: s.Source, Kind: s.Kind, Title: s.Project + " · " + s.Provider, Project: s.Project, Execution: s.Execution, Outcome: s.Outcome, Connectivity: s.Connectivity, Evidence: s.Evidence, Attention: s.Attention, LastActivity: s.LastActivity, StateSince: s.StateSince, ObservedAt: s.ObservedAt, Remote: true, HasActivity: s.HasActivity, OpenTools: s.OpenTools, Buckets: s.Buckets, Capabilities: []string{"observe_remote"}, Aliases: []string{}}
	if IsAttention(s.Attention) {
		o.Summary = "Needs your attention on " + s.Machine
	} else {
		o.Summary = s.Execution + " on " + s.Machine
	}
	return o
}

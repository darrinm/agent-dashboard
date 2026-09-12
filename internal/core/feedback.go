package core

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

var ErrUnknownSession = errors.New("unknown session")
var ErrInvalidFeedback = errors.New("unsupported feedback verdict")

// FeedbackVerdicts are the ways a user can say a session's shown state is wrong.
var FeedbackVerdicts = map[string]string{
	"not_waiting": "Shown as needing you, but isn't",
	"is_waiting":  "Needs you, but isn't shown that way",
	"wrong_state": "Other wrong state",
}

// FeedbackEntry is one line of feedback.jsonl: the user's verdict plus the
// session's full local projection and source health at that moment.
type FeedbackEntry struct {
	At      time.Time `json:"at"`
	Verdict string    `json:"verdict"`
	Note    string    `json:"note,omitempty"`
	Version string    `json:"version"`
	Session Session   `json:"session"`
	Sources []Health  `json:"sources"`
}

// RecordFeedback appends a wrong-state report to feedback.jsonl in the data
// directory. The file contains private session text and never leaves the machine.
func (s *Store) RecordFeedback(sessionID, verdict, note string) error {
	if _, ok := FeedbackVerdicts[verdict]; !ok {
		return ErrInvalidFeedback
	}
	snapshot := s.Snapshot()
	entry := FeedbackEntry{At: time.Now().UTC(), Verdict: verdict, Note: Clip(note, 1000), Version: Version, Sources: snapshot.Sources}
	found := false
	for _, v := range snapshot.Sessions {
		if v.ID == sessionID {
			entry.Session, found = v, true
			break
		}
	}
	if !found {
		return ErrUnknownSession
	}
	f, err := os.OpenFile(filepath.Join(s.DataDir, "feedback.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(wireJSON(entry), '\n'))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// FeedbackReport summarizes feedback.jsonl for an accuracy review.
func FeedbackReport(dir string, w io.Writer) error {
	f, err := os.Open(filepath.Join(dir, "feedback.jsonl"))
	if os.IsNotExist(err) {
		_, err = fmt.Fprintln(w, "No wrong-state reports yet.")
		return err
	}
	if err != nil {
		return err
	}
	defer f.Close()
	var entries []FeedbackEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	for scanner.Scan() {
		var e FeedbackEntry
		if json.Unmarshal(scanner.Bytes(), &e) == nil {
			entries = append(entries, e)
		}
	}
	if err = scanner.Err(); err != nil {
		return err
	}
	type group struct{ verdict, provider string }
	counts := map[group]int{}
	for _, e := range entries {
		counts[group{e.Verdict, e.Session.Provider}]++
	}
	groups := make([]group, 0, len(counts))
	for g := range counts {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].verdict+groups[i].provider < groups[j].verdict+groups[j].provider
	})
	fmt.Fprintf(w, "%d wrong-state reports\n", len(entries))
	for _, g := range groups {
		fmt.Fprintf(w, "  %3d  %s · %s (%s)\n", counts[g], g.verdict, g.provider, FeedbackVerdicts[g.verdict])
	}
	if len(entries) > 0 {
		fmt.Fprintln(w, "\nMost recent:")
	}
	for _, e := range entries[max(0, len(entries)-10):] {
		attention := "none"
		if e.Session.Attention != nil {
			attention = e.Session.Attention.Kind
		}
		fmt.Fprintf(w, "  %s  %-11s  %-6s  %s · attention=%s execution=%s evidence=%s\n",
			e.At.Local().Format("Jan 2 15:04"), e.Verdict, e.Session.Provider, Clip(e.Session.Title, 40), attention, e.Session.Execution, e.Session.Evidence)
	}
	return nil
}

package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// HookBackup records Codex's notify setting from before Agents wrapped it.
type HookBackup struct {
	Notify    []string `json:"notify"`
	HadNotify bool     `json:"hadNotify"`
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func hookCommand(binary, dir string) string {
	return quote(binary) + " hook claude --data-dir " + quote(dir)
}

// Ownership is recognized by data directory rather than binary path, so hooks
// installed before the app moved are still found, replaced, and removed.
func ownedClaudeHook(command, dir string) bool {
	return strings.HasSuffix(command, " hook claude --data-dir "+quote(dir))
}
func ownedCodexWrapper(args []string, dir string) bool {
	return len(args) == 5 && args[1] == "hook" && args[2] == "codex" && args[3] == "--data-dir" && args[4] == dir
}
func InstallHooks(home, dir, binary string, remove bool) (err error) {
	claudePath := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(claudePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	settings := map[string]any{}
	if len(data) > 0 && json.Unmarshal(data, &settings) != nil {
		return errors.New("Claude settings are not valid JSON")
	}
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		hooks = map[string]any{}
	}
	command := hookCommand(binary, dir)
	for _, event := range []string{"Notification", "PermissionRequest", "Stop", "UserPromptSubmit", "SessionStart", "SessionEnd", "PostToolUse"} {
		entries, _ := hooks[event].([]any)
		var keep []any
		for _, entry := range entries {
			m, ok := entry.(map[string]any)
			if !ok {
				keep = append(keep, entry)
				continue
			}
			hs, _ := m["hooks"].([]any)
			var others []any
			for _, h := range hs {
				v, _ := h.(map[string]any)
				if !ownedClaudeHook(str(v, "command"), dir) {
					others = append(others, h)
				}
			}
			if len(others) > 0 {
				m["hooks"] = others
				keep = append(keep, m)
			}
		}
		if !remove {
			keep = append(keep, map[string]any{"matcher": "", "hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 1}}})
		}
		if len(keep) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = keep
		}
	}
	settings["hooks"] = hooks
	codexPath := filepath.Join(home, ".codex", "config.toml")
	codex, err := os.ReadFile(codexPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var config map[string]any
	if len(codex) > 0 && toml.Unmarshal(codex, &config) != nil {
		return errors.New("Codex configuration is not valid TOML")
	}
	backupPath := filepath.Join(dir, "notify-backup.json")
	var backup HookBackup
	existingBackup, _ := os.ReadFile(backupPath)
	haveBackup := len(existingBackup) > 0 && json.Unmarshal(existingBackup, &backup) == nil
	current := stringSlice(config["notify"])
	installed := ownedCodexWrapper(current, dir)
	var replacement []byte
	createBackup := false
	if remove {
		if haveBackup && installed {
			replacement, err = replaceNotify(codex, backup.Notify, backup.HadNotify)
			if err != nil {
				return err
			}
		}
	} else {
		switch {
		case haveBackup && !installed:
			return errors.New("Codex notify changed since installation; remove the prior integration before reinstalling")
		case !haveBackup && installed:
			return errors.New("Codex notify already runs Agents, but the saved original command is missing")
		case !haveBackup:
			backup = HookBackup{Notify: current, HadNotify: config["notify"] != nil}
			createBackup = true
		}
		replacement, err = replaceNotify(codex, []string{binary, "hook", "codex", "--data-dir", dir}, true)
		if err != nil {
			return err
		}
	}
	// Validate both complete results before touching either provider's configuration.
	if replacement != nil {
		var check map[string]any
		if err = toml.Unmarshal(replacement, &check); err != nil {
			return err
		}
	}
	// The wrapper runs the saved original command, so the backup is written before
	// the wrapper exists. A failed installation removes the backup it created, so a
	// retry isn't refused as though Codex's configuration had changed.
	if createBackup {
		if err = AtomicWrite(backupPath, wireJSON(backup)); err != nil {
			return err
		}
		defer func() {
			if err != nil {
				_ = os.Remove(backupPath)
			}
		}()
	}
	pretty, _ := json.MarshalIndent(settings, "", "  ")
	if err = AtomicWrite(claudePath, append(pretty, '\n')); err != nil {
		return err
	}
	if replacement != nil {
		if err = AtomicWrite(codexPath, replacement); err != nil {
			_ = AtomicWrite(claudePath, data)
			return err
		}
	}
	if remove {
		_ = os.Remove(backupPath)
	}
	return nil
}
func stringSlice(v any) []string {
	a, _ := v.([]any)
	out := []string{}
	for _, x := range a {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
func replaceNotify(source []byte, value []string, present bool) ([]byte, error) {
	lines := strings.SplitAfter(string(source), "\n")
	start, end := -1, -1
	offset := 0
	re := regexp.MustCompile(`^\s*notify\s*=`)
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			break
		}
		if re.MatchString(line) {
			start = offset
			var partial string
			for _, l := range lines[i:] {
				partial += l
				var m map[string]any
				if toml.Unmarshal([]byte(partial), &m) == nil {
					end = start + len(partial)
					break
				}
			}
			break
		}
		offset += len(line)
	}
	var text string
	if present {
		text = "notify = " + string(wireJSON(value)) + "\n"
	}
	if start < 0 {
		if !present {
			return source, nil
		}
		return append([]byte(text), source...), nil
	}
	if end < 0 {
		return nil, errors.New("could not safely locate notify array")
	}
	return append(append(append([]byte{}, source[:start]...), []byte(text)...), source[end:]...), nil
}
func ForwardHook(provider, dir string, payload []byte) error {
	var endpoint Endpoint
	b, err := os.ReadFile(filepath.Join(dir, "collector.json"))
	if err != nil {
		return nil
	}
	if json.Unmarshal(b, &endpoint) != nil {
		return nil
	}
	client := &http.Client{Timeout: 200 * time.Millisecond}
	r, err := http.NewRequest("POST", endpoint.URL+"/v1/hooks/"+provider, bytes.NewReader(payload))
	if err != nil {
		return nil
	}
	r.Header.Set("Authorization", "Bearer "+endpoint.Token)
	r.Header.Set("Content-Type", "application/json")
	res, err := client.Do(r)
	if err == nil {
		res.Body.Close()
	}
	return nil
}
func RunHook(provider, dir string, args []string) error {
	if provider == "claude" {
		payload, _ := io.ReadAll(io.LimitReader(os.Stdin, 1024*1024))
		return ForwardHook(provider, dir, payload)
	}
	var backup HookBackup
	b, _ := os.ReadFile(filepath.Join(dir, "notify-backup.json"))
	_ = json.Unmarshal(b, &backup)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if len(args) > 0 {
			_ = ForwardHook("codex", dir, []byte(args[len(args)-1]))
		}
	}()
	var err error
	if len(backup.Notify) > 0 {
		command := exec.Command(backup.Notify[0], append(backup.Notify[1:], args...)...)
		command.Stdin = os.Stdin
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		err = command.Run()
	}
	<-done
	return err
}
func HookPreview(home, dir, binary string) string {
	return fmt.Sprintf("Adds owned lifecycle hooks to %s. Wraps the existing Codex notify command without changing its arguments.\nHook: %s\n", filepath.Join(home, ".claude", "settings.json"), hookCommand(binary, dir))
}

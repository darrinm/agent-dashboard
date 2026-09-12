package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"agents.local/dashboard/internal/core"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	args := os.Args[1:]
	mode := "serve"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		mode = args[0]
		args = args[1:]
	}
	provider := ""
	if mode == "hook" && len(args) > 0 {
		provider = args[0]
		args = args[1:]
	}
	home, _ := os.UserHomeDir()
	name, _ := os.Hostname()
	f := flag.NewFlagSet(mode, flag.ContinueOnError)
	o := core.Options{}
	f.StringVar(&o.Home, "home", home, "Source home directory")
	f.StringVar(&o.DataDir, "data-dir", core.DefaultDir(), "Private state directory")
	f.StringVar(&o.Machine, "machine", name, "Machine display name")
	f.StringVar(&o.Claude, "claude", "claude", "Claude executable")
	f.StringVar(&o.Codex, "codex", "codex", "Codex executable")
	f.StringVar(&o.Listen, "listen", "127.0.0.1:8788", "Hub listen address")
	hub := f.String("hub", "", "Hub URL")
	token := f.String("token", "", "Enrollment token")
	tokenStdin := f.Bool("token-stdin", false, "Read enrollment token from standard input")
	apply := f.Bool("apply", false, "Apply the displayed hook configuration")
	if err := f.Parse(args); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch mode {
	case "version":
		fmt.Println(core.Version)
		return nil
	case "hook":
		return core.RunHook(provider, o.DataDir, f.Args())
	case "hooks-install", "hooks-remove":
		binary, _ := os.Executable()
		fmt.Print(core.HookPreview(o.Home, o.DataDir, binary))
		if !*apply {
			fmt.Println("Run with --apply to update configuration.")
			return nil
		}
		return core.InstallHooks(o.Home, o.DataDir, binary, mode == "hooks-remove")
	case "enroll":
		if *tokenStdin {
			b, e := io.ReadAll(io.LimitReader(os.Stdin, 4096))
			if e != nil {
				return e
			}
			*token = strings.TrimSpace(string(b))
		}
		return core.Enroll(ctx, o.DataDir, *hub, *token, o.Machine)
	case "hub":
		return core.RunHub(ctx, o)
	case "feedback-report":
		return core.FeedbackReport(o.DataDir, os.Stdout)
	case "snapshot":
		s, err := core.OpenStore(o.DataDir, o.Machine)
		if err != nil {
			return err
		}
		defer s.DB.Close()
		d := core.Discovery{Store: s, Home: o.Home, Claude: o.Claude, Codex: o.Codex}
		d.Refresh(ctx)
		return json.NewEncoder(os.Stdout).Encode(s.Snapshot())
	case "serve":
		if err := os.MkdirAll(o.DataDir, 0700); err != nil {
			return err
		}
		lock, err := os.OpenFile(filepath.Join(o.DataDir, "collector.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		defer lock.Close()
		if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			return fmt.Errorf("collector already running for this data directory")
		}
		return core.RunCollector(ctx, o)
	default:
		return fmt.Errorf("unknown command %q", mode)
	}
}

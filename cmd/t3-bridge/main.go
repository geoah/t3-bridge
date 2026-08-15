// t3-bridge connects GitHub issues and PR reviews to t3 coding sessions.
//
//	t3-bridge run     poll forever (the daemon mode used by systemd)
//	t3-bridge once    run a single reconcile tick and exit
//	t3-bridge doctor  check connectivity, auth, and project mapping
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/geoah/t3-bridge/internal/bridge"
	"github.com/geoah/t3-bridge/internal/config"
	"github.com/geoah/t3-bridge/internal/gh"
	"github.com/geoah/t3-bridge/internal/state"
	"github.com/geoah/t3-bridge/internal/t3"
	"github.com/geoah/t3-bridge/internal/ui"
)

func main() {
	configPath := flag.String("config", config.DefaultPath(), "path to config.json")
	flag.Parse()

	cmd := flag.Arg(0)
	if cmd == "" {
		cmd = "run"
	}

	stderrHandler := slog.NewTextHandler(os.Stderr, nil)
	bus := ui.NewBus()
	log := slog.New(ui.NewHandler(stderrHandler, bus))

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal(log, "load config", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "doctor":
		if err := doctor(ctx, cfg); err != nil {
			fatal(log, "doctor", err)
		}
	case "once", "run":
		b, err := newBridge(cfg, log)
		if err != nil {
			fatal(log, "init", err)
		}
		if cmd == "once" {
			if err := b.Tick(ctx); err != nil {
				fatal(log, "tick", err)
			}
			return
		}
		if cfg.UI.Listen != "off" {
			srv := &ui.Server{Bus: bus, Listen: cfg.UI.Listen}
			go func() {
				if err := srv.Serve(); err != nil {
					log.Error("ui server stopped", "err", err)
				}
			}()
			log.Info("monitoring ui listening", "addr", "http://"+cfg.UI.Listen)
		}
		interval := time.Duration(cfg.Poll.IntervalSeconds) * time.Second
		log.Info("t3-bridge running", "interval", interval, "repos", len(cfg.Repos))
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			start := time.Now()
			if err := b.Tick(ctx); err != nil {
				log.Error("tick failed", "err", err)
			}
			bus.SetTick(start, start.Add(interval))
			select {
			case <-ctx.Done():
				log.Info("shutting down")
				return
			case <-ticker.C:
			}
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q (want run, once, or doctor)\n", cmd)
		os.Exit(2)
	}
}

func newBridge(cfg *config.Config, log *slog.Logger) (*bridge.Bridge, error) {
	if _, err := cfg.Token(); err != nil {
		return nil, err
	}
	st, err := state.Load(cfg.State.File)
	if err != nil {
		return nil, err
	}
	return bridge.New(cfg, t3.NewClient(cfg.T3.BaseURL, cfg.Token), gh.New(), st, log), nil
}

func doctor(ctx context.Context, cfg *config.Config) error {
	fmt.Printf("t3 base url: %s\n", cfg.T3.BaseURL)

	probe := t3.NewClient(cfg.T3.BaseURL, nil)
	env, err := probe.Environment(ctx)
	if err != nil {
		return fmt.Errorf("t3 server unreachable: %w", err)
	}
	fmt.Printf("t3 server: version %v, environment %v\n", env["serverVersion"], env["label"])

	if _, err := cfg.Token(); err != nil {
		return err
	}
	t3c := t3.NewClient(cfg.T3.BaseURL, cfg.Token)
	shell, err := t3c.Shell(ctx)
	if err != nil {
		return fmt.Errorf("t3 auth failed (token expired? re-mint with t3 auth session issue): %w", err)
	}
	fmt.Printf("t3 auth: ok (%d projects, %d threads)\n", len(shell.Projects), len(shell.Threads))

	ghc := gh.New()
	viewer, err := ghc.Viewer(ctx)
	if err != nil {
		return fmt.Errorf("gh auth failed (run gh auth login): %w", err)
	}
	fmt.Printf("github auth: ok (%s)\n", viewer)

	ok := true
	for i := range cfg.Repos {
		rc := &cfg.Repos[i]
		var project *t3.Project
		if rc.ProjectID != "" {
			project = shell.ProjectByID(rc.ProjectID)
		} else {
			project = shell.ProjectByWorkspaceRoot(rc.WorkspaceRoot)
		}
		if project == nil {
			ok = false
			fmt.Printf("repo %s: NO matching t3 project (workspaceRoot=%s projectId=%s); create it in t3 first\n",
				rc.Repo, rc.WorkspaceRoot, rc.ProjectID)
			continue
		}
		repo, err := ghc.GetRepo(ctx, rc.Repo)
		if err != nil {
			ok = false
			fmt.Printf("repo %s: github error: %v\n", rc.Repo, err)
			continue
		}
		base := rc.BaseBranch
		if base == "" {
			base = repo.DefaultBranch
		}
		fmt.Printf("repo %s: ok (project %q, assignee %s, base %s, trigger %s)\n",
			rc.Repo, project.Title, rc.Assignee, base, rc.ReviewTrigger)
	}
	if !ok {
		return fmt.Errorf("some repos are misconfigured")
	}
	fmt.Println("all checks passed")
	return nil
}

func fatal(log *slog.Logger, msg string, err error) {
	log.Error(msg, "err", err)
	os.Exit(1)
}

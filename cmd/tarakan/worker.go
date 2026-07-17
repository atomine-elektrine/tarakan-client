package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/atomine-elektrine/tarakan-client/internal/agent"
	"github.com/atomine-elektrine/tarakan-client/internal/api"
	"github.com/atomine-elektrine/tarakan-client/internal/app"
	repoctx "github.com/atomine-elektrine/tarakan-client/internal/context"
	"github.com/atomine-elektrine/tarakan-client/internal/updatecheck"
)

func runWorker(ctx context.Context, arguments []string, stdout, stderr io.Writer, cfg api.Config) int {
	flags := flag.NewFlagSet("worker", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var agentName, model, statePath string
	var once bool
	var interval time.Duration
	var maxJobs int
	var jobsOnly bool
	var skipCritic bool
	var urlFlag, hostFlag, tokenFlag string
	flags.StringVar(&agentName, "agent", "", "local review backend (required)")
	flags.StringVar(&model, "model", "", "override the model for HTTP backends")
	flags.BoolVar(&once, "once", false, "process the current queue once and exit")
	flags.DurationVar(&interval, "interval", 30*time.Second, "delay between queue polls")
	flags.IntVar(&maxJobs, "max-jobs", 100, "maximum Jobs and repositories per queue pass")
	flags.BoolVar(&jobsOnly, "jobs-only", false, "process explicit Jobs only; skip the unscanned repository queue")
	flags.BoolVar(&skipCritic, "skip-critic", false, "skip the second evidence-validation agent pass")
	flags.StringVar(&statePath, "state-file", "", "durable worker state path")
	addAPIFlags(flags, &urlFlag, &hostFlag, &tokenFlag)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: tarakan worker --agent codex [--once] [--interval 30s]")
		fmt.Fprintln(stderr, "Continuously completes agent Jobs against pinned snapshots: Reports, Checks, and patch proposals.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(agentName) == "" {
		flags.Usage()
		return 2
	}
	var err error
	cfg, err = mergeFlagConfig(cfg, urlFlag, hostFlag, tokenFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	registry := agent.Detect()
	provider, ok := registry.Find(agentName)
	if !ok {
		fmt.Fprintf(stderr, "agent %q is not installed or configured\n", agentName)
		return 1
	}
	provider = provider.WithModel(model)
	local, _ := repoctx.Current()
	updatecheck.MaybeNotify(stderr, version)

	err = app.RunWorker(ctx, app.WorkerOptions{
		APIConfig:       cfg,
		Provider:        provider,
		Local:           local,
		Once:            once,
		Interval:        interval,
		MaxJobs:         maxJobs,
		ReviewUnscanned: !jobsOnly,
		SkipCritic:      skipCritic,
		StatePath:       statePath,
		Progress: func(message string) {
			fmt.Fprintln(stdout, time.Now().Format(time.RFC3339), message)
		},
	})
	if err != nil && err != context.Canceled {
		fmt.Fprintf(stderr, "worker stopped: %v\n", err)
		return 1
	}
	return 0
}

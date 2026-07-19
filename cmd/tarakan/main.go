package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/atomine-elektrine/tarakan-client/internal/agent"
	"github.com/atomine-elektrine/tarakan-client/internal/api"
	"github.com/atomine-elektrine/tarakan-client/internal/app"
	repoctx "github.com/atomine-elektrine/tarakan-client/internal/context"
	"github.com/atomine-elektrine/tarakan-client/internal/headless"
	"github.com/atomine-elektrine/tarakan-client/internal/updatecheck"
)

// version is the client release (override with -ldflags "-X main.version=…").
var version = "0.2.2"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	peeledURL, peeledToken, arguments := peelAPIFlags(arguments)
	cfg := api.LoadConfig(peeledURL, peeledToken)
	if len(arguments) > 0 && arguments[0] == "login" {
		return runLogin(arguments[1:], stdout, stderr, cfg, peeledToken)
	}
	if len(arguments) > 0 && arguments[0] == "logout" {
		if len(arguments) != 1 {
			fmt.Fprintln(stderr, "Usage: tarakan logout")
			return 2
		}
		return runLogout(stdout, stderr)
	}

	if len(arguments) > 0 && isWorkCommand(arguments[0]) {
		return runWorkCommand(arguments[0], arguments[1:], stdin, stdout, stderr, cfg)
	}

	flags := flag.NewFlagSet("tarakan", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var prompt string
	var agentName string
	var model string
	var jobID int64
	var pickup bool
	var printContext bool
	var printAgents bool
	var printVersion bool
	var urlFlag, hostFlag, tokenFlag string
	flags.StringVar(&prompt, "p", "", "run one prompt in headless JSON mode")
	flags.StringVar(&prompt, "prompt", "", "run one prompt in headless JSON mode")
	flags.StringVar(&agentName, "agent", "", "review backend: claude, codex, grok, kimi, ollama, openrouter, or kimi-http")
	flags.StringVar(&model, "model", "", "override the model for HTTP backends (ollama, openrouter, kimi-http)")
	flags.Int64Var(&jobID, "job", 0, "open interactive UI, claim this job, and run the agent")
	flags.BoolVar(&pickup, "pickup", false, "open interactive UI, claim next open job from the global queue, run agent")
	flags.BoolVar(&printContext, "context", false, "print repository context as JSON")
	flags.BoolVar(&printAgents, "agents", false, "print detected review backends as JSON")
	flags.BoolVar(&printVersion, "version", false, "print version")
	addAPIFlags(flags, &urlFlag, &hostFlag, &tokenFlag)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Tarakan - public security reports from your terminal")
		fmt.Fprintln(stderr, "\nAuth (saved login; CLI flags and env override it):")
		fmt.Fprintln(stderr, "  tarakan login    Save a token for future commands")
		fmt.Fprintln(stderr, "  tarakan logout   Remove the saved token")
		fmt.Fprintln(stderr, "  --url / --host   Tarakan base URL")
		fmt.Fprintln(stderr, "  --token          One-command API token override")
		fmt.Fprintln(stderr, "\nMass path:")
		fmt.Fprintln(stderr, "  tarakan login")
		fmt.Fprintln(stderr, "  tarakan report --agent grok --pickup")
		fmt.Fprintln(stderr, "  tarakan --url http://localhost:4000 --token TOKEN --agent grok --pickup  # local development")
		fmt.Fprintln(stderr, "  tarakan report --agent grok --job ID --yes")
		fmt.Fprintln(stderr, "  tarakan worker --agent grok")
		fmt.Fprintln(stderr, "  tarakan register owner/name")
		fmt.Fprintln(stderr, "  tarakan register --file repos.txt")
		fmt.Fprintln(stderr, "  tarakan check REPORT_ID --verdict confirmed|disputed --notes TEXT")
		fmt.Fprintln(stderr, "  tarakan check-finding UUID --verdict confirmed|disputed|fixed --notes TEXT")
		fmt.Fprintln(stderr, "  tarakan worker --agent codex   # drains report + check jobs")
		fmt.Fprintln(stderr, "\nInteractive: /url, /token, /config  ·  Also: jobs | claim | submit | …")
		fmt.Fprintln(stderr, "\nUsage: tarakan [options]")
		flags.PrintDefaults()
	}

	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected arguments: %v\n", flags.Args())
		return 2
	}
	if printVersion {
		fmt.Fprintln(stdout, version)
		// Best-effort: show whether a newer release exists (stderr keeps stdout machine-friendly).
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		result, err := updatecheck.Check(ctx, version)
		cancel()
		if err == nil && result.UpdateAvailable {
			fmt.Fprintln(stderr, result.Notice())
		}
		return 0
	}

	resolvedURL, err := resolveAPIFlagURL(urlFlag, hostFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	cfg = cfg.WithOverrides(resolvedURL, tokenFlag)

	repository, err := repoctx.Current()
	if err != nil {
		fmt.Fprintf(stderr, "discover repository: %v\n", err)
		return 1
	}
	registry := agent.Detect()
	if printContext {
		return encodeJSON(stdout, stderr, repository)
	}
	if printAgents {
		return encodeJSON(stdout, stderr, registry.Providers())
	}

	var selected agent.Provider
	if agentName != "" {
		var ok bool
		selected, ok = registry.Find(agentName)
		if !ok {
			fmt.Fprintf(stderr, "backend %q is not installed or configured\n", agentName)
			return 1
		}
	} else {
		selected, _ = registry.Default()
	}
	selected = selected.WithModel(model)

	if prompt != "" {
		if jobID > 0 || pickup {
			fmt.Fprintln(stderr, "use either -p/--prompt or --job/--pickup, not both")
			return 2
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if selected.Name == "" {
			selected = agent.Provider{Name: "unavailable", Description: "No supported agent"}
		}
		if err := headless.Run(ctx, stdout, repository, selected, prompt); err != nil {
			if !errors.Is(err, context.Canceled) {
				fmt.Fprintf(stderr, "review failed: %v\n", err)
			}
			return 1
		}
		return 0
	}

	if (jobID > 0 || pickup) && selected.Name == "" {
		fmt.Fprintln(stderr, "no review backend available; install grok/codex/claude/kimi or pass --agent")
		return 1
	}

	// Surfaces "newer release available" before long agent runs / TUI work.
	updatecheck.MaybeNotify(stderr, version)

	program := tea.NewProgram(app.NewSession(repository, registry, selected, app.SessionOpts{
		JobID:     jobID,
		Pickup:    pickup,
		APIConfig: cfg,
	}))
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(stderr, "run Tarakan: %v\n", err)
		return 1
	}
	return 0
}

func encodeJSON(stdout, stderr io.Writer, value any) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintf(stderr, "encode JSON: %v\n", err)
		return 1
	}
	return 0
}

// runInteractiveJob opens the TUI. If jobID > 0, claims that job; if pickup,
// claims the next open report job for this repo. Then runs the agent and waits
// for /submit-report.
func runInteractiveJob(agentName, model string, jobID int64, pickup bool, cfg api.Config, stderr io.Writer) int {
	repository, err := repoctx.Current()
	if err != nil {
		fmt.Fprintf(stderr, "discover repository: %v\n", err)
		return 1
	}
	registry := agent.Detect()
	var selected agent.Provider
	if agentName != "" {
		var ok bool
		selected, ok = registry.Find(agentName)
		if !ok {
			fmt.Fprintf(stderr, "backend %q is not installed or configured\n", agentName)
			return 1
		}
	} else {
		selected, _ = registry.Default()
	}
	selected = selected.WithModel(model)
	if selected.Name == "" {
		fmt.Fprintln(stderr, "no review backend available; install grok/codex/claude/kimi or pass --agent")
		return 1
	}

	program := tea.NewProgram(app.NewSession(repository, registry, selected, app.SessionOpts{
		JobID:     jobID,
		Pickup:    pickup,
		APIConfig: cfg,
	}))
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(stderr, "run Tarakan: %v\n", err)
		return 1
	}
	return 0
}

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/atomine-elektrine/tarakan-client/internal/api"
)

func runRegister(ctx context.Context, arguments []string, stdout, stderr io.Writer, cfg api.Config) int {
	flags := flag.NewFlagSet("register", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var fromFile string
	var sleepMS int
	var urlFlag, hostFlag, tokenFlag string
	flags.StringVar(&fromFile, "file", "", "register every owner/name line in this file")
	flags.IntVar(&sleepMS, "sleep-ms", 250, "delay between registrations (rate-limit friendly)")
	addAPIFlags(flags, &urlFlag, &hostFlag, &tokenFlag)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: tarakan register owner/name [owner/name ...]")
		fmt.Fprintln(stderr, "       tarakan register --file repos.txt")
		fmt.Fprintln(stderr, "Register public GitHub repositories with Tarakan (idempotent).")
		flags.PrintDefaults()
	}
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	var err error
	cfg, err = mergeFlagConfig(cfg, urlFlag, hostFlag, tokenFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	targets := flags.Args()
	if fromFile != "" {
		raw, readErr := os.ReadFile(fromFile)
		if readErr != nil {
			fmt.Fprintf(stderr, "read %s: %v\n", fromFile, readErr)
			return 1
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			targets = append(targets, line)
		}
	}
	if len(targets) == 0 {
		flags.Usage()
		return 2
	}

	client, err := cfg.Client()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	ok, fail := 0, 0
	for i, target := range targets {
		if i > 0 && sleepMS > 0 {
			select {
			case <-ctx.Done():
				fmt.Fprintln(stderr, ctx.Err())
				return 1
			case <-time.After(time.Duration(sleepMS) * time.Millisecond):
			}
		}
		repo, regErr := client.RegisterRepository(ctx, target)
		if regErr != nil {
			fail++
			fmt.Fprintf(stderr, "err  %s → %v\n", target, regErr)
			continue
		}
		ok++
		slug := repo.Slug()
		if slug == "" {
			slug = target
		}
		fmt.Fprintf(stdout, "ok   %s  status=%s  %s\n", slug, repo.Status, repo.RecordURL)
	}
	fmt.Fprintf(stderr, "done: %d registered, %d failed\n", ok, fail)
	if fail > 0 {
		return 1
	}
	return 0
}

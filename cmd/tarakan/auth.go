package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/atomine-elektrine/tarakan-client/internal/api"
	"github.com/atomine-elektrine/tarakan-client/internal/browser"
)

func runLogin(arguments []string, stdout, stderr io.Writer, config api.Config, explicitToken string) int {
	flags := flag.NewFlagSet("login", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var noBrowser bool
	var clientName string
	flags.BoolVar(&noBrowser, "no-browser", false, "print the approval URL without opening a browser")
	flags.StringVar(&clientName, "name", defaultClientName(), "name shown on the web approval screen")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: tarakan login [--url URL] [--no-browser]")
		fmt.Fprintln(stderr, "       tarakan login --token TOKEN  # manual fallback")
		flags.PrintDefaults()
	}
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		flags.Usage()
		return 2
	}

	if token := strings.TrimSpace(explicitToken); token != "" {
		return saveLogin(stdout, stderr, config, token)
	}

	client, err := api.NewPublic(config.BaseURL, nil)
	if err != nil {
		fmt.Fprintf(stderr, "start web login: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	authorization, err := client.StartDeviceAuthorization(ctx, clientName)
	if err != nil {
		fmt.Fprintf(stderr, "start web login: %v\n", err)
		fmt.Fprintln(stderr, "If this server predates web login, use `tarakan login --token TOKEN`.")
		return 1
	}

	fmt.Fprintf(stdout, "Confirm code %s in your browser:\n%s\n", authorization.UserCode, authorization.VerificationURIComplete)
	if !noBrowser {
		if err := browser.Open(authorization.VerificationURIComplete); err != nil {
			fmt.Fprintf(stderr, "Could not open a browser automatically: %v\n", err)
			fmt.Fprintln(stderr, "Open the URL shown above to continue.")
		} else {
			fmt.Fprintln(stdout, "Waiting for browser approval…")
		}
	}

	interval := time.Duration(authorization.Interval) * time.Second
	if interval < time.Second {
		interval = 2 * time.Second
	}
	deadline := time.Duration(authorization.ExpiresIn) * time.Second
	if deadline <= 0 {
		deadline = 10 * time.Minute
	}
	pollCtx, stopPolling := context.WithTimeout(ctx, deadline)
	defer stopPolling()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		credential, err := client.ExchangeDeviceAuthorization(pollCtx, authorization.DeviceCode)
		switch {
		case err == nil && strings.TrimSpace(credential.Token) != "":
			return saveLogin(stdout, stderr, config, credential.Token)
		case err == nil:
			fmt.Fprintln(stderr, "finish web login: server returned an empty credential")
			return 1
		case errors.Is(err, api.ErrAuthorizationPending):
			select {
			case <-pollCtx.Done():
				fmt.Fprintln(stderr, "Web login expired. Run `tarakan login` to try again.")
				return 1
			case <-ticker.C:
			}
		case errors.Is(err, api.ErrAccessDenied):
			fmt.Fprintln(stderr, "Web login was denied.")
			return 1
		case errors.Is(err, api.ErrDeviceCodeExpired):
			fmt.Fprintln(stderr, "Web login expired. Run `tarakan login` to try again.")
			return 1
		default:
			fmt.Fprintf(stderr, "finish web login: %v\n", err)
			return 1
		}
	}
}

func saveLogin(stdout, stderr io.Writer, config api.Config, token string) int {
	config = config.WithOverrides("", token)
	path, err := api.SaveConfig(config)
	if err != nil {
		fmt.Fprintf(stderr, "save login: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Logged in to %s. Credentials saved to %s (mode 0600).\n", config.BaseURL, path)
	return 0
}

func runLogout(stdout, stderr io.Writer) int {
	if saved, err := api.LoadSavedConfig(); err == nil && saved.Token != "" {
		if client, err := saved.Client(); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err = client.RevokeCurrentCredential(ctx)
			cancel()
			if err != nil {
				fmt.Fprintf(stderr, "warning: could not revoke server credential: %v\n", err)
				fmt.Fprintln(stderr, "You can still revoke it from Tarakan account settings.")
			}
		}
	}
	if err := api.RemoveSavedConfig(); err != nil {
		fmt.Fprintf(stderr, "log out: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Logged out. Saved Tarakan credentials removed.")
	return 0
}

func defaultClientName() string {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		return "Tarakan Client"
	}
	return "Tarakan Client on " + hostname
}

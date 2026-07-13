package main

import (
	"flag"
	"fmt"
	"strings"

	"tarakan-client/internal/api"
)

// peelAPIFlags pulls --url/--host/--token from anywhere in args so they work
// before or after the subcommand name:
//
//	tarakan --token SECRET report --pickup
//	tarakan report --token SECRET --pickup
func peelAPIFlags(args []string) (url, token string, rest []string) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--url" || arg == "--host":
			if i+1 >= len(args) {
				rest = append(rest, arg)
				continue
			}
			i++
			url = args[i]
		case strings.HasPrefix(arg, "--url="):
			url = strings.TrimPrefix(arg, "--url=")
		case strings.HasPrefix(arg, "--host="):
			url = strings.TrimPrefix(arg, "--host=")
		case arg == "--token":
			if i+1 >= len(args) {
				rest = append(rest, arg)
				continue
			}
			i++
			token = args[i]
		case strings.HasPrefix(arg, "--token="):
			token = strings.TrimPrefix(arg, "--token=")
		default:
			rest = append(rest, arg)
		}
	}
	return url, token, rest
}

func addAPIFlags(fs *flag.FlagSet, url, host, token *string) {
	fs.StringVar(url, "url", "", "Tarakan host URL (overrides saved login and $TARAKAN_URL)")
	fs.StringVar(host, "host", "", "alias for --url")
	fs.StringVar(token, "token", "", "API token (overrides saved login and $TARAKAN_API_TOKEN)")
}

func resolveAPIFlagURL(url, host string) (string, error) {
	url = strings.TrimSpace(url)
	host = strings.TrimSpace(host)
	switch {
	case url != "" && host != "" && url != host:
		return "", fmt.Errorf("--url and --host disagree (%q vs %q)", url, host)
	case url != "":
		return url, nil
	default:
		return host, nil
	}
}

func apiConfigFromFlags(url, host, token string) (api.Config, error) {
	resolved, err := resolveAPIFlagURL(url, host)
	if err != nil {
		return api.Config{}, err
	}
	return api.LoadConfig(resolved, token), nil
}

package app

import "strings"

type command struct {
	name string
	args []string
}

func parseCommand(input string) (command, bool) {
	if !strings.HasPrefix(input, "/") {
		return command{}, false
	}
	fields := strings.Fields(strings.TrimPrefix(input, "/"))
	if len(fields) == 0 {
		return command{}, false
	}
	return command{name: strings.ToLower(fields[0]), args: fields[1:]}, true
}

package browser

import (
	"os/exec"
	"runtime"
)

// Open launches target in the operating system's default browser.
func Open(target string) error {
	var command string
	var arguments []string
	switch runtime.GOOS {
	case "darwin":
		command = "open"
		arguments = []string{target}
	case "windows":
		command = "rundll32"
		arguments = []string{"url.dll,FileProtocolHandler", target}
	default:
		command = "xdg-open"
		arguments = []string{target}
	}
	cmd := exec.Command(command, arguments...)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

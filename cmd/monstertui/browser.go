package main

import (
	"os/exec"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"
)

// openBrowserCmd opens url in the system's default browser as a
// bubbletea command, reporting failure via errMsg.
func openBrowserCmd(url string) tea.Cmd {
	return func() tea.Msg {
		if err := openURL(url); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// openURL launches the OS's "open this" command for url. It's run via
// exec.Command with url as a separate argument (never through a shell),
// so it's safe even though url comes from untrusted relay data.
func openURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

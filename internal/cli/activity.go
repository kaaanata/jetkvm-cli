package cli

import (
	"io"

	"github.com/kaaanata/jetkvm-cli/internal/progress"
	"github.com/kaaanata/jetkvm-cli/internal/terminal"
	"github.com/spf13/cobra"
)

func (a *App) startActivity(cmd *cobra.Command) error {
	mode, err := a.resolvedOutputMode()
	if err != nil {
		return err
	}
	if !a.executing {
		return nil
	}
	for parent := cmd; parent != nil; parent = parent.Parent() {
		if parent.Name() == "mcp" || parent.Name() == "completion" || parent.Name() == "__complete" {
			return nil
		}
	}
	if mode != "text" {
		return nil
	}
	a.activity = terminal.NewActivity(a.deps.Stderr, a.deps.IsTerminal(a.deps.Stdout) && a.deps.IsTerminal(a.deps.Stderr))
	cmd.SetContext(progress.WithObserver(cmd.Context(), a.activity.Report))
	if logger, ok := a.deps.Logger.(interface{ SetOutput(io.Writer) }); ok {
		logger.SetOutput(a.activity)
	}
	return nil
}

func (a *App) pauseActivity() func() {
	if a.activity == nil {
		return func() {}
	}
	a.activity.Pause()
	return a.activity.Resume
}

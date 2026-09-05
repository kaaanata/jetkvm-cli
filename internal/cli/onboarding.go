package cli

import (
	"context"
	"errors"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/kaaanata/jetkvm-cli/internal/onboarding"
	"github.com/kaaanata/jetkvm-cli/internal/terminal"
	"github.com/spf13/cobra"
)

func (a *App) canGuideDevice() bool {
	return a.outputMode != "json" && a.deps.InputTerminal(a.deps.Stdin) && a.deps.IsTerminal(a.deps.Stdout) && a.deps.IsTerminal(a.deps.Stderr)
}

func (a *App) deviceSetupNeeded() (bool, error) {
	service, err := onboarding.New(onboarding.Options{Path: a.configPath})
	if err != nil {
		return false, err
	}
	return service.Needed()
}

func (a *App) guideDevice(ctx context.Context) (receipt onboarding.Receipt, err error) {
	if !a.canGuideDevice() {
		return receipt, config.ErrMissing
	}
	resume := a.pauseActivity()
	defer resume()
	service, err := onboarding.New(onboarding.Options{Path: a.configPath})
	if err != nil {
		return receipt, err
	}
	browser, err := onboarding.NewBrowser(service)
	if err != nil {
		return receipt, err
	}
	defer func() { err = errors.Join(err, browser.Close()) }()
	p, err := browser.Begin(onboarding.Draft{})
	if err != nil {
		return receipt, err
	}
	renderer := terminal.New(a.deps.Stderr, a.deps.IsTerminal(a.deps.Stderr))
	if err := renderer.Write(terminal.Document{Title: "Connect your JetKVM", Sections: []terminal.Section{{Text: "Complete the local setup page. Only your device address is needed; any password stays on this computer."}, {Title: "Setup page", Text: p.URL}, {Text: "Waiting for setup. Press Ctrl+C to cancel."}}}); err != nil {
		return receipt, err
	}
	if a.deps.OpenBrowser != nil {
		_ = a.deps.OpenBrowser(ctx, p.URL)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	return browser.Wait(ctx, p.ID)
}

func (a *App) newDeviceSetupCommand() *cobra.Command {
	return &cobra.Command{Use: "device", Short: "Connect a device with guided local setup", Args: noArgs, Annotations: map[string]string{"runtime": "skip"}, RunE: func(cmd *cobra.Command, _ []string) error {
		receipt, err := a.guideDevice(cmd.Context())
		if err != nil {
			return err
		}
		return a.writeResult("setup.device", receipt)
	}}
}

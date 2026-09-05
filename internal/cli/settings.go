package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/kaaanata/jetkvm-cli/internal/onboarding"
	"github.com/spf13/cobra"
)

func (a *App) settingsService() (*onboarding.Service, error) {
	if a.configPath == "" {
		return nil, config.ErrMissing
	}
	path, err := filepath.Abs(a.configPath)
	if err != nil {
		return nil, err
	}
	return onboarding.New(onboarding.Options{Path: path})
}

func (a *App) newConfigCommand() *cobra.Command {
	command := &cobra.Command{Use: "config", Short: "Read and update JetKVM settings", Args: noArgs}
	show := &cobra.Command{Use: "show", Short: "Read settings without exposing credentials", Args: noArgs, Annotations: map[string]string{"runtime": "skip"}, RunE: func(cmd *cobra.Command, _ []string) error {
		service, err := a.settingsService()
		if err != nil {
			return err
		}
		settings, err := service.Settings()
		if err != nil {
			return err
		}
		return a.writeResult("config.show", settings)
	}}
	var target, output, idle, lifetime string
	var globalInput, deviceInput, exposed, takeover, yes bool
	set := &cobra.Command{Use: "set", Short: "Review and save explicit settings changes", Args: noArgs, Annotations: map[string]string{"runtime": "skip"}, RunE: func(cmd *cobra.Command, _ []string) error {
		service, err := a.settingsService()
		if err != nil {
			return err
		}
		settings, err := service.Settings()
		if err != nil {
			return err
		}
		patch := onboarding.SettingsPatch{ExpectedRevision: settings.Revision}
		if cmd.Flags().Changed("default-output") {
			patch.Output = new(config.OutputMode(output))
		}
		if cmd.Flags().Changed("enable-input") {
			patch.InputEnabled = new(globalInput)
		}
		dp := &onboarding.DevicePatch{}
		if cmd.Flags().Changed("input") {
			dp.InputEnabled = new(deviceInput)
		}
		if cmd.Flags().Changed("exposed") {
			dp.Exposed = new(exposed)
		}
		if cmd.Flags().Changed("takeover") {
			dp.TakeoverAllowed = new(takeover)
		}
		if cmd.Flags().Changed("idle-timeout") {
			dp.IdleTimeout = new(idle)
		}
		if cmd.Flags().Changed("absolute-lifetime") {
			dp.AbsoluteLifetime = new(lifetime)
		}
		deviceChanged := dp.InputEnabled != nil || dp.Exposed != nil || dp.TakeoverAllowed != nil || dp.IdleTimeout != nil || dp.AbsoluteLifetime != nil
		if deviceChanged {
			if target == "" {
				return usageError(fmt.Errorf("--device is required for device settings"))
			}
			matches := 0
			for _, device := range settings.Devices {
				if device.DeviceID == target || device.Name == target {
					dp.DeviceID = device.DeviceID
					matches++
				}
			}
			if matches != 1 {
				return usageError(fmt.Errorf("select one configured device using its name or device ID"))
			}
			patch.Device = dp
		} else if target != "" {
			return usageError(fmt.Errorf("--device requires a device setting to change"))
		}
		before, after, err := service.Preview(patch)
		if err != nil {
			return err
		}
		if !yes && !a.canGuideDevice() {
			return ErrConfirmationRequired
		}
		var summary strings.Builder
		summary.WriteString("Save these JetKVM settings?\n")
		for _, change := range onboarding.SettingChanges(before, after) {
			fmt.Fprintf(&summary, "\n%s: %s -> %s", change.Field, change.Before, change.After)
		}
		if err := a.confirmMaintenance(summary.String(), yes); err != nil {
			return err
		}
		receipt, err := service.Update(cmd.Context(), patch)
		if err != nil {
			return err
		}
		return a.writeResult("config.set", receipt)
	}}
	set.Flags().StringVar(&target, "device", "", "configured device name or stable ID")
	set.Flags().StringVar(&output, "default-output", "", "default result format: auto, text, or json")
	set.Flags().BoolVar(&globalInput, "enable-input", false, "explicit global keyboard and mouse permission")
	set.Flags().BoolVar(&deviceInput, "input", false, "keyboard and mouse permission for --device")
	set.Flags().BoolVar(&exposed, "exposed", false, "expose --device to clients")
	set.Flags().BoolVar(&takeover, "takeover", false, "allow session takeover for --device; confirmation remains required")
	set.Flags().StringVar(&idle, "idle-timeout", "", "idle control timeout for --device, for example 5m")
	set.Flags().StringVar(&lifetime, "absolute-lifetime", "", "absolute control lifetime for --device, for example 30m")
	set.Flags().BoolVarP(&yes, "yes", "y", false, "approve these exact settings without prompting")
	command.AddCommand(show, set)
	return command
}

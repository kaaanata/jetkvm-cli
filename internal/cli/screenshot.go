package cli

import (
	"context"
	"errors"
	"os"
	"time"
	"uuid"

	"github.com/kaaanata/jetkvm-cli/internal/automation"
	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/policy"
	"github.com/kaaanata/jetkvm-cli/internal/progress"
	"github.com/kaaanata/jetkvm-cli/internal/video"
	"github.com/spf13/cobra"
)

// Observer is optional so control-only adapters do not need video support.
type Observer interface {
	Observe(context.Context, automation.ObserveRequest) (automation.ScreenObservation, error)
}

type screenshotResult struct {
	Wake        *automation.WakeReceipt `json:"wake,omitempty"`
	ImageBase64 string                  `json:"image_base64,omitempty"`
	Observation video.Observation       `json:"observation"`
	MIMEType    string                  `json:"mime_type"`
	File        string                  `json:"file,omitempty"`
}

func screenResult(screen automation.ScreenObservation, file string) (*screenshotResult, error) {
	result := &screenshotResult{Observation: screen.Observation, MIMEType: screen.MIMEType, Wake: screen.Wake}
	if file != "" {
		if err := os.WriteFile(file, screen.Data, 0600); err != nil {
			return result, err
		}
		result.File = file
	}
	return result, nil
}

func (a *App) newScreenshotCommand() *cobra.Command {
	var file string
	var freshness time.Duration
	var disableWake bool
	command := &cobra.Command{
		Use: "screenshot <device>", Aliases: []string{"observe"}, Short: "Capture a PNG screenshot using a temporary control", Args: exactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if file == "" {
				return usageError(errors.New("--file is required"))
			}
			if freshness < 0 {
				return usageError(errors.New("--freshness must not be negative"))
			}
			observer, ok := a.deps.Automation.(Observer)
			if !ok {
				return domain.ErrCapabilityUnavailable
			}
			deviceID, err := a.resolveDevice(command.Context(), args[0])
			if err != nil {
				return err
			}
			capabilities := []string{"video"}
			if planner, ok := a.deps.Automation.(interface {
				CanWake(domain.DeviceID, policy.Scope) bool
			}); ok && !disableWake && planner.CanWake(deviceID, policy.Scope{}) {
				capabilities = append(capabilities, "input")
			}
			return a.withEphemeralControl(command.Context(), deviceID, capabilities, func(ctx context.Context, ref control.Ref) error {
				progress.Stage(ctx, "Waiting for a fresh screen")
				screen, err := observer.Observe(ctx, automation.ObserveRequest{
					ControlRequest: automation.ControlRequest{DeviceID: deviceID, Ref: ref, Scope: policy.Scope{}},
					Freshness:      freshness, DisableWake: disableWake, WakeOperationID: uuid.NewV7(),
				})
				if err != nil {
					if screen.Wake != nil {
						return errors.Join(a.writeResult("screenshot", &screenshotResult{Wake: screen.Wake}), err)
					}
					return err
				}
				progress.Stage(ctx, "Saving screenshot")
				result, err := screenResult(screen, file)
				if err != nil {
					if screen.Wake != nil {
						return errors.Join(a.writeResult("screenshot", result), err)
					}
					return err
				}
				return a.writeResult("screenshot", result)
			})
		},
	}
	command.Flags().BoolVar(&disableWake, "no-wake", false, "disable automatic waking for sleeping/no-signal video")
	command.Flags().StringVar(&file, "file", "", "explicit path for the PNG screenshot")
	command.Flags().DurationVar(&freshness, "freshness", 0, "maximum observation age; zero uses the service default")
	return command
}

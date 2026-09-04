package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/automation"
	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/policy"
	"github.com/kaaanata/jetkvm-cli/internal/video"
	"github.com/spf13/cobra"
)

// Observer is optional so control-only adapters do not need video support.
type Observer interface {
	Observe(context.Context, automation.ObserveRequest) (automation.ScreenObservation, error)
}

type screenshotResult struct {
	ImageBase64 string            `json:"image_base64,omitempty"`
	Observation video.Observation `json:"observation"`
	MIMEType    string            `json:"mime_type"`
	File        string            `json:"file,omitempty"`
}

func screenResult(screen automation.ScreenObservation, file string) (*screenshotResult, error) {
	result := &screenshotResult{Observation: screen.Observation, MIMEType: screen.MIMEType}
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
			return a.withEphemeralControl(command.Context(), deviceID, []string{"video"}, func(ctx context.Context, ref control.Ref) error {
				screen, err := observer.Observe(ctx, automation.ObserveRequest{
					ControlRequest: automation.ControlRequest{DeviceID: deviceID, Ref: ref, Scope: policy.Scope{}},
					Freshness:      freshness,
				})
				if err != nil {
					return err
				}
				result, err := screenResult(screen, file)
				if err != nil {
					return err
				}
				return a.writeResult("screenshot", result, func(w io.Writer) error { _, err := fmt.Fprintln(w, result.File); return err })
			})
		},
	}
	command.Flags().StringVar(&file, "file", "", "explicit path for the PNG screenshot")
	command.Flags().DurationVar(&freshness, "freshness", 0, "maximum observation age; zero uses the service default")
	return command
}

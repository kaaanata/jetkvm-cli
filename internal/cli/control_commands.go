package cli

import (
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/kaaanata/jetkvm-cli/internal/automation"
	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/input"
	"github.com/kaaanata/jetkvm-cli/internal/operation"
	"github.com/kaaanata/jetkvm-cli/internal/policy"
	"github.com/kaaanata/jetkvm-cli/internal/progress"
	"github.com/spf13/cobra"
)

var (
	ErrConfirmationRequired    = errors.New("action-time confirmation is required")
	ErrConfirmationUnavailable = errors.New("confirmation issuer is unavailable")
)

type boundFlags struct {
	handle      string
	generation  uint64
	operationID string
}

func (f *boundFlags) addRef(command *cobra.Command) {
	command.Flags().StringVar(&f.handle, "handle", "", "advanced: existing in-process control handle (must be paired with --generation)")
	command.Flags().Uint64Var(&f.generation, "generation", 0, "advanced: expected generation for --handle")
	_ = command.Flags().MarkHidden("handle")
	_ = command.Flags().MarkHidden("generation")
}

func (f *boundFlags) addOperation(command *cobra.Command) {
	f.addRef(command)
	command.Flags().StringVar(&f.operationID, "operation-id", "", "operation UUID; generated when omitted")
}

func (f boundFlags) ref() (control.Ref, error) {
	if strings.TrimSpace(f.handle) == "" || f.generation == 0 {
		return control.Ref{}, usageError(errors.New("--handle and non-zero --generation are required"))
	}
	return control.Ref{ID: control.HandleID(f.handle), ExpectedGeneration: f.generation}, nil
}

func (f boundFlags) optionalRef() (control.Ref, bool, error) {
	hasHandle := strings.TrimSpace(f.handle) != ""
	hasGeneration := f.generation != 0
	if hasHandle != hasGeneration {
		return control.Ref{}, false, usageError(errors.New("--handle and --generation must be supplied together"))
	}
	if !hasHandle {
		return control.Ref{}, false, nil
	}
	return control.Ref{ID: control.HandleID(f.handle), ExpectedGeneration: f.generation}, true, nil
}

func (f boundFlags) operation() (uuid.UUID, error) {
	if f.operationID == "" {
		return uuid.NewV7(), nil
	}
	id, err := uuid.Parse(f.operationID)
	if err != nil || id == uuid.Nil() {
		return uuid.Nil(), usageError(fmt.Errorf("invalid --operation-id %q", f.operationID))
	}
	return id, nil
}

func (a *App) newControlCommand() *cobra.Command {
	command := &cobra.Command{Use: "control", Short: "Manage explicit JetKVM control sessions", Args: noArgs}
	command.AddCommand(a.newControlOpenCommand(), a.newControlStatusCommand(), a.newControlCloseCommand())
	return command
}

func (a *App) withEphemeralControl(ctx context.Context, deviceID domain.DeviceID, capabilities []string, execute func(context.Context, control.Ref) error) (err error) {
	service, err := a.automation()
	if err != nil {
		return err
	}
	request := automation.OpenControlRequest{DeviceID: deviceID, Capabilities: capabilities, Scope: policy.Scope{}, Ownership: control.OwnershipOwned}
	plan, err := service.PrepareOpenControl(request)
	if err != nil {
		return err
	}
	executionContext := ctx
	if plan.Required {
		executionContext, err = a.confirm(ctx, ConfirmationRequest{
			DeviceID: deviceID, Action: "control.takeover",
			Summary: "open a temporary JetKVM control session; this may disconnect an active browser session",
			Binding: plan.Binding,
		})
		if err != nil {
			return err
		}
	}
	progress.Stage(executionContext, "Connecting to device")
	handle, err := service.OpenControl(executionContext, request)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil && a.activity != nil {
			a.failureStage = a.activity.Stage()
		}
		progress.Stage(ctx, "Releasing temporary control")
		closeCtx, cancel := context.WithTimeoutCause(context.Background(), 5*time.Second, errors.New("temporary control cleanup timed out"))
		defer cancel()
		_, closeErr := service.CloseControl(closeCtx, automation.ControlRequest{
			DeviceID: deviceID, Ref: control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation}, Scope: policy.Scope{},
		})
		err = errors.Join(err, closeErr)
	}()
	return execute(executionContext, control.Ref{ID: handle.ID, ExpectedGeneration: handle.Generation})
}

func (a *App) newControlOpenCommand() *cobra.Command {
	capabilities := []string{"input"}
	idleTimeout := control.DefaultIdleTimeout
	absoluteLifetime := control.DefaultAbsoluteLifetime
	command := &cobra.Command{
		Use:   "open <device>",
		Short: "Open an owned, generation-fenced control session",
		Args:  exactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			service, err := a.automation()
			if err != nil {
				return err
			}
			deviceID, err := a.resolveDevice(command.Context(), args[0])
			if err != nil {
				return err
			}
			capabilities = normalizeStrings(capabilities)
			if len(capabilities) == 0 {
				return usageError(errors.New("at least one --capability is required"))
			}
			for _, capability := range capabilities {
				switch capability {
				case "input", "power", "video":
				default:
					return usageError(fmt.Errorf("unsupported control capability %q", capability))
				}
			}
			request := automation.OpenControlRequest{
				DeviceID:         deviceID,
				Capabilities:     capabilities,
				Scope:            policy.Scope{},
				Ownership:        control.OwnershipOwned,
				IdleTimeout:      idleTimeout,
				AbsoluteLifetime: absoluteLifetime,
			}
			plan, err := service.PrepareOpenControl(request)
			if err != nil {
				return err
			}
			executionContext := command.Context()
			if plan.Required {
				executionContext, err = a.confirm(executionContext, ConfirmationRequest{
					DeviceID: deviceID, Action: "control.takeover",
					Summary: "open a JetKVM control session; this may disconnect an active browser session",
					Binding: plan.Binding,
				})
				if err != nil {
					return err
				}
			}
			handle, err := service.OpenControl(executionContext, request)
			if err != nil {
				return err
			}
			result := makeControlHandleResult(handle)
			return a.writeResult("control.open", result)
		},
	}
	command.Flags().StringSliceVar(&capabilities, "capability", capabilities, "requested capability: input, power, or video")
	command.Flags().DurationVar(&idleTimeout, "idle-timeout", idleTimeout, "control idle timeout")
	command.Flags().DurationVar(&absoluteLifetime, "absolute-lifetime", absoluteLifetime, "maximum control lifetime")
	return command
}

func (a *App) newControlStatusCommand() *cobra.Command {
	flags := new(boundFlags)
	command := &cobra.Command{
		Use:   "status <device>",
		Short: "Read a control handle without creating a connection",
		Args:  exactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			service, err := a.automation()
			if err != nil {
				return err
			}
			request, err := a.controlRequest(command.Context(), args[0], *flags)
			if err != nil {
				return err
			}
			snapshot, err := service.GetControl(command.Context(), request)
			if err != nil {
				return err
			}
			result := makeControlSnapshotResult(snapshot)
			return a.writeResult("control.status", result)
		},
	}
	flags.addRef(command)
	return command
}

func (a *App) newControlCloseCommand() *cobra.Command {
	flags := new(boundFlags)
	command := &cobra.Command{
		Use:   "close <device>",
		Short: "Neutralize input and close an owned control session",
		Args:  exactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			service, err := a.automation()
			if err != nil {
				return err
			}
			request, err := a.controlRequest(command.Context(), args[0], *flags)
			if err != nil {
				return err
			}
			handle, err := service.CloseControl(command.Context(), request)
			if err != nil {
				return err
			}
			result := makeControlHandleResult(handle)
			return a.writeResult("control.close", result)
		},
	}
	flags.addRef(command)
	return command
}

func (a *App) newInputCommand() *cobra.Command {
	command := &cobra.Command{Use: "input", Short: "Send bounded keyboard and pointer input", Args: noArgs}
	command.AddCommand(
		a.newInputKeyCommand(),
		a.newInputTypeCommand(),
		a.newPointerCommand("move", "Move the pointer using a bound observation", input.ActionMove),
		a.newPointerCommand("click", "Click using a bound observation", input.ActionClick),
		a.newPointerCommand("double-click", "Double-click using a bound observation", input.ActionDoubleClick),
		a.newInputDragCommand(),
		a.newInputScrollCommand(),
		a.newInputRunCommand(),
		a.newInputReleaseCommand(),
	)
	for _, child := range command.Commands() {
		if child.Name() == "release" {
			continue
		}
		child.Flags().Bool("observe-after", false, "capture the screen after the action batch; requires --file or --image-base64")
		child.Flags().String("file", "", "save the resulting screenshot as PNG; implies --observe-after")
		child.Flags().Bool("image-base64", false, "include screenshot PNG as base64 in JSON; implies --observe-after")
	}
	return command
}

func (a *App) newInputKeyCommand() *cobra.Command {
	flags := new(boundFlags)
	command := &cobra.Command{
		Use:   "key <device> <key> [key...]",
		Short: "Press and release a key or chord",
		Args:  minimumArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			action := input.Action{Type: input.ActionKeypress, Keys: slices.Clone(args[1:])}
			return a.runInputActions(command, args[0], *flags, []input.Action{action}, nil, inputConfirmation(action))
		},
	}
	flags.addOperation(command)
	return command
}

func (a *App) newInputTypeCommand() *cobra.Command {
	flags := new(boundFlags)
	var text string
	var fromStdin bool
	command := &cobra.Command{
		Use:   "type <device>",
		Short: "Type fully prevalidated US-layout text",
		Args:  exactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if command.Flags().Changed("text") == fromStdin {
				return usageError(errors.New("exactly one of --text or --text-stdin is required"))
			}
			value := text
			if fromStdin {
				var err error
				value, err = readBoundedText(a.deps.Stdin)
				if err != nil {
					return err
				}
			}
			action := input.Action{Type: input.ActionTypeText, Text: value}
			return a.runInputActions(command, args[0], *flags, []input.Action{action}, nil, inputConfirmation(action))
		},
	}
	flags.addOperation(command)
	command.Flags().StringVar(&text, "text", "", "printable US-layout text (visible in process arguments)")
	command.Flags().BoolVar(&fromStdin, "text-stdin", false, "read text from stdin without trimming")
	return command
}

type observationFlags struct {
	id         string
	width      int
	height     int
	capturedAt string
}

func (f *observationFlags) add(command *cobra.Command) {
	command.Flags().StringVar(&f.id, "observation-id", "", "observation ID used for coordinate binding")
	command.Flags().IntVar(&f.width, "frame-width", 0, "bound observation frame width")
	command.Flags().IntVar(&f.height, "frame-height", 0, "bound observation frame height")
	command.Flags().StringVar(&f.capturedAt, "observation-captured-at", "", "bound observation timestamp in RFC3339")
}

func (f observationFlags) binding(generation uint64, required bool) (*input.ObservationBinding, error) {
	if f.id == "" && f.width == 0 && f.height == 0 && f.capturedAt == "" && !required {
		return nil, nil
	}
	if f.id == "" {
		return nil, nil
	}
	return &input.ObservationBinding{ID: f.id, Generation: generation}, nil
}

func (a *App) newPointerCommand(name, short string, actionType input.ActionType) *cobra.Command {
	flags := new(boundFlags)
	observation := new(observationFlags)
	var x, y int
	button := string(input.ButtonLeft)
	command := &cobra.Command{
		Use:   name + " <device>",
		Short: short,
		Args:  exactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if !command.Flags().Changed("x") || !command.Flags().Changed("y") {
				return usageError(errors.New("--x and --y are required"))
			}
			binding, err := observation.binding(flags.generation, true)
			if err != nil {
				return err
			}
			action := input.Action{Type: actionType, X: x, Y: y}
			if actionType != input.ActionMove {
				action.Button = input.Button(button)
			}
			return a.runInputActions(command, args[0], *flags, []input.Action{action}, binding, confirmationNone)
		},
	}
	flags.addOperation(command)
	observation.add(command)
	command.Flags().IntVar(&x, "x", 0, "horizontal pixel coordinate")
	command.Flags().IntVar(&y, "y", 0, "vertical pixel coordinate")
	if actionType != input.ActionMove {
		command.Flags().StringVar(&button, "button", button, "pointer button: left, right, middle, back, or forward")
	}
	return command
}

func (a *App) newInputDragCommand() *cobra.Command {
	flags := new(boundFlags)
	observation := new(observationFlags)
	var pathJSON string
	button := string(input.ButtonLeft)
	command := &cobra.Command{
		Use:   "drag <device>",
		Short: "Drag through a strictly decoded observation-bound path",
		Args:  exactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			path, err := decodePoints(pathJSON)
			if err != nil {
				return err
			}
			binding, err := observation.binding(flags.generation, true)
			if err != nil {
				return err
			}
			action := input.Action{Type: input.ActionDrag, Path: path, Button: input.Button(button)}
			return a.runInputActions(command, args[0], *flags, []input.Action{action}, binding, confirmationNone)
		},
	}
	flags.addOperation(command)
	observation.add(command)
	command.Flags().StringVar(&pathJSON, "path-json", "", `JSON array of points, for example [{"x":10,"y":20},{"x":30,"y":40}]`)
	command.Flags().StringVar(&button, "button", button, "pointer button: left, right, middle, back, or forward")
	return command
}

func (a *App) newInputScrollCommand() *cobra.Command {
	flags := new(boundFlags)
	var deltaX, deltaY int
	command := &cobra.Command{
		Use:   "scroll <device>",
		Short: "Send one bounded horizontal or vertical scroll step",
		Args:  exactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			action := input.Action{Type: input.ActionScroll, DeltaX: deltaX, DeltaY: deltaY}
			return a.runInputActions(command, args[0], *flags, []input.Action{action}, nil, confirmationNone)
		},
	}
	flags.addOperation(command)
	command.Flags().IntVar(&deltaX, "delta-x", 0, "horizontal scroll delta in -127..127")
	command.Flags().IntVar(&deltaY, "delta-y", 0, "vertical scroll delta in -127..127")
	return command
}

func (a *App) newInputRunCommand() *cobra.Command {
	flags := new(boundFlags)
	observation := new(observationFlags)
	var actionsJSON, actionsFile string
	command := &cobra.Command{
		Use:   "run <device>",
		Short: "Run a bounded, deterministic JSON action batch",
		Args:  exactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			payload, err := readJSONArgument(actionsJSON, actionsFile)
			if err != nil {
				return err
			}
			actions, needsObservation, err := decodeActions(payload)
			if err != nil {
				return err
			}
			binding, err := observation.binding(flags.generation, needsObservation)
			if err != nil {
				return err
			}
			return a.runInputActions(command, args[0], *flags, actions, binding, batchConfirmation(actions))
		},
	}
	flags.addOperation(command)
	observation.add(command)
	command.Flags().StringVar(&actionsJSON, "actions-json", "", "strict JSON action array")
	command.Flags().StringVar(&actionsFile, "actions-file", "", "path to strict JSON action array, or - for stdin")
	return command
}

func (a *App) newInputReleaseCommand() *cobra.Command {
	flags := new(boundFlags)
	command := &cobra.Command{
		Use:   "release <device>",
		Short: "Explicitly neutralize keyboard and pointer state",
		Args:  exactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if a.deps.Releaser == nil {
				return unavailableDependency("input release service")
			}
			deviceID, err := a.resolveDevice(command.Context(), args[0])
			if err != nil {
				return err
			}
			operationID, err := flags.operation()
			if err != nil {
				return err
			}
			execute := func(ctx context.Context, ref control.Ref) error {
				receipt, err := a.deps.Releaser.ReleaseInput(ctx, automation.ReleaseInputRequest{DeviceID: deviceID, Ref: ref, Scope: policy.Scope{}, OperationID: operationID})
				if err != nil {
					return err
				}
				result := makeOperationReceiptResult(receipt)
				return a.writeResult("input.release", result)
			}
			ref, supplied, err := flags.optionalRef()
			if err != nil {
				return err
			}
			if supplied {
				return execute(command.Context(), ref)
			}
			return a.withEphemeralControl(command.Context(), deviceID, []string{"input"}, execute)
		},
	}
	flags.addOperation(command)
	return command
}

func (a *App) newPowerCommand() *cobra.Command {
	command := &cobra.Command{Use: "power", Short: "Read ATX state and execute explicit power actions", Args: noArgs}
	command.AddCommand(a.newPowerStatusCommand())
	for _, definition := range []struct {
		name   string
		action automation.PowerAction
		risk   confirmationRisk
	}{
		{name: "press", action: automation.PowerPress},
		{name: "reset", action: automation.PowerReset, risk: confirmationRequired},
		{name: "hold", action: automation.PowerHold, risk: confirmationRequired},
	} {
		command.AddCommand(a.newPowerActionCommand(definition.name, definition.action, definition.risk))
	}
	return command
}

func (a *App) newPowerStatusCommand() *cobra.Command {
	flags := new(boundFlags)
	command := &cobra.Command{
		Use:   "status <device>",
		Short: "Read ATX extension and LED state",
		Args:  exactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			service, err := a.automation()
			if err != nil {
				return err
			}
			deviceID, err := a.resolveDevice(command.Context(), args[0])
			if err != nil {
				return err
			}
			execute := func(ctx context.Context, ref control.Ref) error {
				progress.Stage(ctx, "Reading power state")
				state, err := service.GetPowerState(ctx, automation.ControlRequest{DeviceID: deviceID, Ref: ref, Scope: policy.Scope{}})
				if err != nil {
					return err
				}
				return a.writeResult("power.status", state)
			}
			ref, supplied, err := flags.optionalRef()
			if err != nil {
				return err
			}
			if supplied {
				return execute(command.Context(), ref)
			}
			return a.withEphemeralControl(command.Context(), deviceID, []string{"power"}, execute)
		},
	}
	flags.addRef(command)
	return command
}

func (a *App) newPowerActionCommand(name string, action automation.PowerAction, _ confirmationRisk) *cobra.Command {
	flags := new(boundFlags)
	command := &cobra.Command{
		Use:   name + " <device>",
		Short: "Execute ATX power " + name + " without automatic retry",
		Args:  exactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			service, err := a.automation()
			if err != nil {
				return err
			}
			deviceID, err := a.resolveDevice(command.Context(), args[0])
			if err != nil {
				return err
			}
			operationID, err := flags.operation()
			if err != nil {
				return err
			}
			execute := func(executionContext context.Context, ref control.Ref) error {
				request := automation.PowerActionRequest{DeviceID: deviceID, Ref: ref, Scope: policy.Scope{}, OperationID: operationID, Action: action}
				plan, err := service.PreparePowerAction(request)
				if err != nil {
					return err
				}
				if plan.Required {
					executionContext, err = a.confirm(executionContext, ConfirmationRequest{
						DeviceID: deviceID, Ref: ref, OperationID: operationID,
						Action: "power." + name, Summary: "execute ATX power " + name, Binding: plan.Binding,
					})
					if err != nil {
						return err
					}
				}
				progress.Stage(executionContext, "Executing authorized power action")
				receipt, err := service.PowerAction(executionContext, request)
				if err != nil {
					return err
				}
				result := makeOperationReceiptResult(receipt)
				return a.writeResult("power."+name, result)
			}
			ref, supplied, err := flags.optionalRef()
			if err != nil {
				return err
			}
			if supplied {
				return execute(command.Context(), ref)
			}
			return a.withEphemeralControl(command.Context(), deviceID, []string{"power"}, execute)
		},
	}
	flags.addOperation(command)
	return command
}

func (a *App) automation() (AutomationService, error) {
	if a.deps.Automation == nil {
		return nil, unavailableDependency("automation service")
	}
	return a.deps.Automation, nil
}

func (a *App) controlRequest(ctx context.Context, selector string, flags boundFlags) (automation.ControlRequest, error) {
	deviceID, err := a.resolveDevice(ctx, selector)
	if err != nil {
		return automation.ControlRequest{}, err
	}
	ref, err := flags.ref()
	if err != nil {
		return automation.ControlRequest{}, err
	}
	return automation.ControlRequest{DeviceID: deviceID, Ref: ref, Scope: policy.Scope{}}, nil
}

func (a *App) boundOperation(ctx context.Context, selector string, flags boundFlags) (domain.DeviceID, control.Ref, uuid.UUID, error) {
	deviceID, err := a.resolveDevice(ctx, selector)
	if err != nil {
		return "", control.Ref{}, uuid.Nil(), err
	}
	ref, err := flags.ref()
	if err != nil {
		return "", control.Ref{}, uuid.Nil(), err
	}
	operationID, err := flags.operation()
	if err != nil {
		return "", control.Ref{}, uuid.Nil(), err
	}
	return deviceID, ref, operationID, nil
}

func (a *App) runInputActions(command *cobra.Command, selector string, flags boundFlags, actions []input.Action, observation *input.ObservationBinding, _ confirmationRisk) error {
	observeAfter, _ := command.Flags().GetBool("observe-after")
	file, _ := command.Flags().GetString("file")
	includeImage, _ := command.Flags().GetBool("image-base64")
	if observeAfter && file == "" && !includeImage {
		return usageError(errors.New("--observe-after requires --file or --image-base64"))
	}
	observeAfter = observeAfter || file != "" || includeImage
	service, err := a.automation()
	if err != nil {
		return err
	}
	deviceID, err := a.resolveDevice(command.Context(), selector)
	if err != nil {
		return err
	}
	operationID, err := flags.operation()
	if err != nil {
		return err
	}
	needsObservation, needsVideo := false, observeAfter
	for _, action := range actions {
		switch action.Type {
		case input.ActionMove, input.ActionClick, input.ActionDoubleClick, input.ActionDrag:
			needsObservation, needsVideo = true, true
		case input.ActionScreenshot:
			needsVideo = true
		}
	}
	ref, supplied, err := flags.optionalRef()
	if err != nil {
		return err
	}
	if !supplied && observation != nil {
		return usageError(errors.New("--observation-id requires an existing --handle; temporary controls capture their own observation"))
	}
	execute := func(executionContext context.Context, ref control.Ref) error {
		boundObservation := observation
		if needsObservation && boundObservation == nil {
			observer, ok := service.(Observer)
			if !ok {
				return domain.ErrCapabilityUnavailable
			}
			screen, err := observer.Observe(executionContext, automation.ObserveRequest{
				ControlRequest: automation.ControlRequest{DeviceID: deviceID, Ref: ref, Scope: policy.Scope{}},
			})
			if err != nil {
				return err
			}
			meta := screen.Observation
			boundObservation = &input.ObservationBinding{ID: meta.ID, Generation: meta.Frame.Generation, Width: meta.Frame.Width, Height: meta.Frame.Height, CapturedAt: meta.CapturedAt}
		}
		request := automation.RunActionsRequest{
			DeviceID: deviceID, Ref: ref, Scope: policy.Scope{}, OperationID: operationID,
			Batch:        input.Batch{Observation: boundObservation, Actions: slices.Clone(actions)},
			ObserveAfter: observeAfter,
		}
		plan, err := service.PrepareRunActions(request)
		if err != nil {
			return err
		}
		if plan.Required {
			executionContext, err = a.confirm(executionContext, ConfirmationRequest{
				DeviceID: deviceID, Ref: ref, OperationID: operationID,
				Action: "input.commit", Summary: inputSummary(actions), Binding: plan.Binding,
			})
			if err != nil {
				return err
			}
		}
		progress.Stage(executionContext, "Executing input actions")
		result, runErr := service.RunActions(executionContext, request)
		view := makeRunActionsResult(result)
		if result.Observation != nil {
			view.Observation, err = screenResult(*result.Observation, file)
			if includeImage {
				view.Observation.ImageBase64 = base64.StdEncoding.EncodeToString(result.Observation.Data)
			}
			if err != nil {
				runErr = errors.Join(runErr, err)
			}
		}
		if runErr != nil && result.Operation.ID == uuid.Nil() {
			return runErr
		}
		return errors.Join(runErr, a.writeResult("input.run", view))
	}
	if supplied {
		return execute(command.Context(), ref)
	}
	capabilities := []string{"input"}
	if needsVideo {
		capabilities = append(capabilities, "video")
	}
	return a.withEphemeralControl(command.Context(), deviceID, capabilities, execute)
}

func (a *App) confirm(ctx context.Context, request ConfirmationRequest) (context.Context, error) {
	resume := a.pauseActivity()
	defer resume()
	request.Interactive = a.deps.IsTerminal(a.deps.Stderr)
	if a.deps.Confirmations == nil {
		if request.Interactive {
			return nil, ErrConfirmationUnavailable
		}
		return nil, ErrConfirmationRequired
	}
	confirmedContext, err := a.deps.Confirmations.Issue(ctx, request)
	if err != nil {
		return nil, err
	}
	if confirmedContext == nil {
		return nil, ErrConfirmationUnavailable
	}
	return confirmedContext, nil
}

type confirmationRisk bool

const (
	confirmationNone     confirmationRisk = false
	confirmationRequired confirmationRisk = true
)

func inputConfirmation(action input.Action) confirmationRisk {
	if action.Type == input.ActionTypeText && utf8.RuneCountInString(action.Text) > 256 {
		return confirmationRequired
	}
	if action.Type != input.ActionKeypress {
		return confirmationNone
	}
	for _, key := range action.Keys {
		normalized := normalizeKey(key)
		if strings.Contains(normalized, "CTRL") || strings.Contains(normalized, "CONTROL") || strings.Contains(normalized, "ALT") || strings.Contains(normalized, "META") || strings.Contains(normalized, "SUPER") || strings.Contains(normalized, "COMMAND") {
			return confirmationRequired
		}
	}
	return confirmationNone
}

func batchConfirmation(actions []input.Action) confirmationRisk {
	seenType := false
	for _, action := range actions {
		if inputConfirmation(action) == confirmationRequired {
			return confirmationRequired
		}
		if action.Type == input.ActionTypeText {
			seenType = true
			continue
		}
		if seenType && action.Type == input.ActionKeypress {
			for _, key := range action.Keys {
				normalized := normalizeKey(key)
				if normalized == "ENTER" || normalized == "RETURN" || strings.HasPrefix(normalized, "F") {
					return confirmationRequired
				}
			}
		}
	}
	return confirmationNone
}

func normalizeKey(key string) string {
	replacer := strings.NewReplacer("-", "", "_", "", " ", "")
	return replacer.Replace(strings.ToUpper(strings.TrimSpace(key)))
}

func inputSummary(actions []input.Action) string {
	runes := 0
	for _, action := range actions {
		if action.Type == input.ActionTypeText {
			runes += utf8.RuneCountInString(action.Text)
		}
	}
	if runes > 0 {
		return fmt.Sprintf("execute %d input action(s), including %d typed character(s)", len(actions), runes)
	}
	return fmt.Sprintf("execute %d input action(s)", len(actions))
}

func normalizeStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result = append(result, value)
		}
	}
	slices.Sort(result)
	return slices.Compact(result)
}

func minimumArgs(count int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) < count {
			return usageError(fmt.Errorf("requires at least %d argument(s), received %d", count, len(args)))
		}
		for _, argument := range args {
			if strings.TrimSpace(argument) == "" {
				return usageError(errors.New("arguments must not be empty"))
			}
		}
		return nil
	}
}

func readBoundedText(reader io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(reader, input.MaxTextRunes+1))
	if err != nil {
		return "", fmt.Errorf("read text from stdin: %w", err)
	}
	if len(data) > input.MaxTextRunes {
		return "", usageError(fmt.Errorf("stdin text exceeds %d bytes", input.MaxTextRunes))
	}
	return string(data), nil
}

func readJSONArgument(inline, path string) ([]byte, error) {
	if (inline == "") == (path == "") {
		return nil, usageError(errors.New("exactly one of --actions-json or --actions-file is required"))
	}
	if inline != "" {
		return []byte(inline), nil
	}
	if path == "-" {
		return nil, usageError(errors.New("--actions-file=- is unavailable when stdin is reserved for typed input; use --actions-json"))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read actions file: %w", err)
	}
	return data, nil
}

type actionJSON struct {
	Type       input.ActionType `json:"type"`
	X          *int             `json:"x,omitempty"`
	Y          *int             `json:"y,omitempty"`
	Button     string           `json:"button,omitempty"`
	Path       []input.Point    `json:"path,omitempty"`
	DeltaX     *int             `json:"delta_x,omitempty"`
	DeltaY     *int             `json:"delta_y,omitempty"`
	Keys       []string         `json:"keys,omitempty"`
	Text       *string          `json:"text,omitempty"`
	DurationMS *int64           `json:"duration_ms,omitempty"`
}

func decodeActions(data []byte) ([]input.Action, bool, error) {
	var encoded []actionJSON
	if err := json.Unmarshal(data, &encoded, json.RejectUnknownMembers(true)); err != nil {
		return nil, false, usageError(fmt.Errorf("decode actions JSON: %w", err))
	}
	if len(encoded) == 0 || len(encoded) > input.MaxActions {
		return nil, false, usageError(fmt.Errorf("actions must contain 1..%d items", input.MaxActions))
	}
	actions := make([]input.Action, 0, len(encoded))
	needsObservation := false
	for index, item := range encoded {
		action, coordinate, err := item.action()
		if err != nil {
			return nil, false, usageError(fmt.Errorf("action %d: %w", index, err))
		}
		needsObservation = needsObservation || coordinate
		actions = append(actions, action)
	}
	return actions, needsObservation, nil
}

func (a actionJSON) action() (input.Action, bool, error) {
	result := input.Action{Type: a.Type, Button: input.Button(a.Button), Path: slices.Clone(a.Path), Keys: slices.Clone(a.Keys)}
	coordinate := false
	switch a.Type {
	case input.ActionMove, input.ActionClick, input.ActionDoubleClick:
		if a.X == nil || a.Y == nil || a.DeltaX != nil || a.DeltaY != nil || len(a.Path) > 0 || len(a.Keys) > 0 || a.Text != nil || a.DurationMS != nil {
			return input.Action{}, false, errors.New("coordinate action requires only x, y, and optional button")
		}
		if a.Type == input.ActionMove && a.Button != "" {
			return input.Action{}, false, errors.New("move does not accept button")
		}
		result.X, result.Y, coordinate = *a.X, *a.Y, true
	case input.ActionDrag:
		if a.X != nil || a.Y != nil || len(a.Path) == 0 || a.DeltaX != nil || a.DeltaY != nil || len(a.Keys) > 0 || a.Text != nil || a.DurationMS != nil {
			return input.Action{}, false, errors.New("drag requires path and optional button")
		}
		coordinate = true
	case input.ActionScroll:
		if a.DeltaX == nil || a.DeltaY == nil || a.X != nil || a.Y != nil || a.Button != "" || len(a.Path) > 0 || len(a.Keys) > 0 || a.Text != nil || a.DurationMS != nil {
			return input.Action{}, false, errors.New("scroll requires only delta_x and delta_y")
		}
		result.DeltaX, result.DeltaY = *a.DeltaX, *a.DeltaY
	case input.ActionKeypress:
		if len(a.Keys) == 0 || a.X != nil || a.Y != nil || a.Button != "" || len(a.Path) > 0 || a.DeltaX != nil || a.DeltaY != nil || a.Text != nil || a.DurationMS != nil {
			return input.Action{}, false, errors.New("keypress requires only keys")
		}
	case input.ActionTypeText:
		if a.Text == nil || a.X != nil || a.Y != nil || a.Button != "" || len(a.Path) > 0 || a.DeltaX != nil || a.DeltaY != nil || len(a.Keys) > 0 || a.DurationMS != nil {
			return input.Action{}, false, errors.New("type requires only text")
		}
		result.Text = *a.Text
	case input.ActionWait:
		if a.DurationMS == nil || a.X != nil || a.Y != nil || a.Button != "" || len(a.Path) > 0 || a.DeltaX != nil || a.DeltaY != nil || len(a.Keys) > 0 || a.Text != nil {
			return input.Action{}, false, errors.New("wait requires only duration_ms")
		}
		result.Duration = time.Duration(*a.DurationMS) * time.Millisecond
	case input.ActionScreenshot:
		if a.X != nil || a.Y != nil || a.Button != "" || len(a.Path) > 0 || a.DeltaX != nil || a.DeltaY != nil || len(a.Keys) > 0 || a.Text != nil || a.DurationMS != nil {
			return input.Action{}, false, errors.New("screenshot accepts no additional fields")
		}
	default:
		return input.Action{}, false, fmt.Errorf("unknown action type %q", a.Type)
	}
	return result, coordinate, nil
}

func decodePoints(data string) ([]input.Point, error) {
	if data == "" {
		return nil, usageError(errors.New("--path-json is required"))
	}
	var points []input.Point
	if err := json.Unmarshal([]byte(data), &points, json.RejectUnknownMembers(true)); err != nil {
		return nil, usageError(fmt.Errorf("decode path JSON: %w", err))
	}
	if len(points) < 2 || len(points) > 64 {
		return nil, usageError(errors.New("drag path must contain 2..64 points"))
	}
	return points, nil
}

type controlHandleResult struct {
	HandleID          control.HandleID    `json:"control_handle"`
	DeviceID          domain.DeviceID     `json:"device_id"`
	Generation        uint64              `json:"generation"`
	Ownership         control.Ownership   `json:"ownership"`
	Capabilities      []string            `json:"capabilities"`
	State             control.HandleState `json:"state"`
	CreatedAt         time.Time           `json:"created_at"`
	LastUsedAt        time.Time           `json:"last_used_at"`
	IdleExpiresAt     time.Time           `json:"idle_expires_at"`
	AbsoluteExpiresAt time.Time           `json:"absolute_expires_at"`
}

func makeControlHandleResult(handle control.Handle) controlHandleResult {
	return controlHandleResult{
		HandleID: handle.ID, DeviceID: handle.DeviceID, Generation: handle.Generation,
		Ownership: handle.Ownership, Capabilities: slices.Clone(handle.Capabilities), State: handle.State,
		CreatedAt: handle.CreatedAt, LastUsedAt: handle.LastUsedAt,
		IdleExpiresAt: handle.IdleExpiresAt, AbsoluteExpiresAt: handle.AbsoluteExpiresAt,
	}
}

type controlSnapshotResult struct {
	Transport control.TransportState `json:"transport"`
	Session   control.SessionState   `json:"session"`
	Handle    *controlHandleResult   `json:"handle,omitempty"`
}

func makeControlSnapshotResult(snapshot control.Snapshot) controlSnapshotResult {
	result := controlSnapshotResult{Transport: snapshot.Transport, Session: snapshot.Session}
	if snapshot.Handle != nil {
		result.Handle = new(makeControlHandleResult(*snapshot.Handle))
	}
	return result
}

type operationReceiptResult struct {
	OperationID       uuid.UUID                    `json:"operation_id"`
	DeviceID          domain.DeviceID              `json:"device_id"`
	ControlGeneration uint64                       `json:"control_generation"`
	Effect            domain.EffectClass           `json:"effect"`
	Action            string                       `json:"action"`
	Stage             operation.Stage              `json:"stage"`
	Delivery          operation.Delivery           `json:"delivery"`
	Verification      operation.VerificationStatus `json:"verification"`
	TerminalClaim     string                       `json:"terminal_claim"`
	RetrySafe         bool                         `json:"retry_safe"`
	ErrorKind         string                       `json:"error_kind,omitempty"`
	Warnings          []string                     `json:"warnings,omitempty"`
	CreatedAt         time.Time                    `json:"created_at"`
	UpdatedAt         time.Time                    `json:"updated_at"`
}

func makeOperationReceiptResult(receipt operation.Receipt) operationReceiptResult {
	return operationReceiptResult{
		OperationID: receipt.ID, DeviceID: receipt.DeviceID, ControlGeneration: receipt.ControlGeneration,
		Effect: receipt.Effect, Action: receipt.Action, Stage: receipt.Stage, Delivery: receipt.Delivery,
		Verification: receipt.Verification.Status, TerminalClaim: receipt.TerminalClaim, RetrySafe: receipt.RetrySafe,
		ErrorKind: receipt.ErrorKind, Warnings: slices.Clone(receipt.Warnings),
		CreatedAt: receipt.CreatedAt, UpdatedAt: receipt.UpdatedAt,
	}
}

type runActionsResult struct {
	Observation *screenshotResult      `json:"observation,omitempty"`
	Operation   operationReceiptResult `json:"operation"`
	Batch       input.BatchReceipt     `json:"batch,omitzero"`
	Existing    bool                   `json:"existing,omitzero"`
}

func makeRunActionsResult(result automation.RunActionsResult) runActionsResult {
	return runActionsResult{Operation: makeOperationReceiptResult(result.Operation), Batch: result.Batch, Existing: result.Existing}
}

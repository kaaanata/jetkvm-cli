package cli

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"

	"github.com/kaaanata/jetkvm-cli/internal/config"
	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/input"
	"github.com/kaaanata/jetkvm-cli/internal/onboarding"
	"github.com/kaaanata/jetkvm-cli/internal/operation"
	setupcore "github.com/kaaanata/jetkvm-cli/internal/setup"
	"github.com/kaaanata/jetkvm-cli/internal/terminal"
	updatecore "github.com/kaaanata/jetkvm-cli/internal/update"
	"github.com/kaaanata/jetkvm-cli/internal/video"
)

type resultEnvelope struct {
	SchemaVersion string `json:"schema_version"`
	Command       string `json:"command"`
	Data          any    `json:"data"`
}

type failureEnvelope struct {
	SchemaVersion string        `json:"schema_version"`
	Error         failureDetail `json:"error"`
}

type failureDetail struct {
	Kind      string `json:"kind"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	ExitCode  int    `json:"exit_code"`
}

type usageFailure struct{ err error }

func (e *usageFailure) Error() string { return e.err.Error() }
func (e *usageFailure) Unwrap() error { return e.err }

func usageError(err error) error { return &usageFailure{err: err} }

func unavailableDependency(name string) error {
	return fmt.Errorf("%s is not configured", name)
}

type pendingResult struct {
	command string
	data    any
}

func (a *App) writeResult(command string, data any) error {
	if a.executing {
		a.pending = append(a.pending, pendingResult{command, data})
		return nil
	}
	return a.emitResult(command, data, false)
}

func (a *App) emitResult(command string, data any, partial bool) error {
	mode, err := a.resolvedOutputMode()
	if err != nil {
		return err
	}
	if mode == "text" {
		document, err := resultDocument(command, data)
		if err != nil {
			return err
		}
		if !a.verbose && !partial {
			document = compactDocument(document)
		}
		if partial {
			document.Title = "Partial result — command did not complete"
			document.Tone = "attention"
		}
		return terminal.New(a.deps.Stdout, a.deps.IsTerminal(a.deps.Stdout)).Write(document)
	}
	return writeJSON(a.deps.Stdout, resultEnvelope{
		SchemaVersion: "v1",
		Command:       command,
		Data:          data,
	})
}

func (a *App) resolvedOutputMode() (string, error) {
	switch a.outputMode {
	case outputAuto:
		return a.defaultOutputMode(), nil
	case "json", "text":
		return a.outputMode, nil
	default:
		return "", usageError(fmt.Errorf("invalid output format %q; expected json or text", a.outputMode))
	}
}

func (a *App) defaultOutputMode() string {
	if a.deps.IsTerminal(a.deps.Stdout) {
		return "text"
	}
	return "json"
}

func writeJSON(w io.Writer, value any) error {
	encoded, err := json.Marshal(value, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return fmt.Errorf("encode JSON result: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := w.Write(encoded); err != nil {
		return fmt.Errorf("write JSON result: %w", err)
	}
	return nil
}

func renderFailure(w io.Writer, mode string, err error, tty bool) error {
	return renderFailureAt(w, mode, err, tty, "")
}

func renderFailureAt(w io.Writer, mode string, err error, tty bool, stage string) error {
	detail := classifyFailure(err)
	if mode == "json" {
		return writeJSON(w, failureEnvelope{SchemaVersion: "v1", Error: detail})
	}
	document := terminal.Document{Title: "Error [" + detail.Kind + "]", Failure: true,
		Sections: []terminal.Section{{Text: detail.Message}}}
	if stage != "" {
		document.Sections = append(document.Sections, terminal.Fields("", terminal.Row("last stage", stage)))
	}
	if hint := recoveryHint(detail.Kind); hint != "" {
		document.Sections = append(document.Sections, terminal.Section{Title: "Next", Text: hint})
	}
	if detail.ExitCode == ExitUsage {
		document.Sections = append(document.Sections, terminal.Section{Text: "Use --help for usage."})
	}
	return terminal.New(w, tty).Write(document)
}

// ExitCode maps command failures to the stable process exit contract.
func ExitCode(err error) int { return classifyFailure(err).ExitCode }

func classifyFailure(err error) failureDetail {
	detail := failureDetail{Kind: "internal", Message: safeErrorMessage(err), ExitCode: ExitInternal}
	_, isUsage := errors.AsType[*usageFailure](err)
	switch {
	case errors.Is(err, onboarding.ErrPolicyDenied):
		detail.Kind, detail.ExitCode = "configuration_denied", ExitAuth
	case errors.Is(err, onboarding.ErrInvalid):
		detail.Kind, detail.ExitCode = "invalid_argument", ExitUsage
	case errors.Is(err, onboarding.ErrRevisionConflict), errors.Is(err, onboarding.ErrConflict):
		detail.Kind, detail.ExitCode = "configuration_conflict", ExitConflict
	case errors.Is(err, onboarding.ErrActiveControls):
		detail.Kind, detail.ExitCode = "configuration_busy", ExitConflict
	case errors.Is(err, config.ErrMissing):
		detail.Kind, detail.ExitCode = "configuration_required", ExitUsage
		detail.Message = "Connect your JetKVM through your agent, or run jetkvm setup device in an interactive terminal."
	case isUsage:
		detail.Kind, detail.ExitCode = "invalid_argument", ExitUsage
	case errors.Is(err, updatecore.ErrRollbackFailed):
		detail.Kind, detail.ExitCode = string(updatecore.ErrRollbackFailed), ExitAmbiguous
	case errors.Is(err, updatecore.ErrApplyFailed):
		detail.Kind, detail.ExitCode = string(updatecore.ErrApplyFailed), ExitUnavailable
	case errors.Is(err, updatecore.ErrSignatureVerification), errors.Is(err, updatecore.ErrChecksumMismatch):
		detail.Kind, detail.ExitCode = "update_verification_failed", ExitAuth
	case errors.Is(err, domain.ErrDeviceNotExposed):
		detail.Kind, detail.ExitCode = "device_not_exposed", ExitNotFound
	case errors.Is(err, domain.ErrDeviceIdentityMismatch):
		detail.Kind, detail.ExitCode = "device_identity_mismatch", ExitNotFound
	case errors.Is(err, domain.ErrCapabilityUnavailable), errors.Is(err, video.ErrDecoderUnavailable):
		detail.Kind, detail.ExitCode = "capability_unavailable", ExitUnsupported
	case errors.Is(err, video.ErrFrameStale), errors.Is(err, input.ErrObservationStale):
		detail.Kind, detail.ExitCode = "observation_stale", ExitConflict
	case errors.Is(err, video.ErrVideoUnavailable), errors.Is(err, video.ErrPipelineClosed), errors.Is(err, video.ErrDecodeFailed), errors.Is(err, video.ErrDimensionsExceeded):
		detail.Kind, detail.ExitCode = "unavailable", ExitUnavailable
	case errors.Is(err, domain.ErrFirmwareUnsupported):
		detail.Kind, detail.ExitCode = "firmware_unsupported", ExitUnsupported
	case errors.Is(err, domain.ErrTakeoverDisabled):
		detail.Kind, detail.ExitCode = "takeover_disabled", ExitConflict
	case errors.Is(err, ErrConfirmationRequired):
		detail.Kind, detail.ExitCode = "confirmation_required", ExitAuth
	case errors.Is(err, ErrConfirmationUnavailable):
		detail.Kind, detail.ExitCode = "confirmation_unavailable", ExitAuth
	case errors.Is(err, control.ErrControlNotFound):
		detail.Kind, detail.ExitCode = "control_not_found", ExitNotFound
	case errors.Is(err, control.ErrControlExpired):
		detail.Kind, detail.ExitCode = "control_expired", ExitConflict
	case errors.Is(err, control.ErrGenerationMismatch), errors.Is(err, input.ErrStaleGeneration), errors.Is(err, video.ErrGenerationMismatch):
		detail.Kind, detail.ExitCode = "control_generation_mismatch", ExitConflict
	case errors.Is(err, operation.ErrConflict):
		detail.Kind, detail.ExitCode = "operation_conflict", ExitConflict
	case errors.Is(err, input.ErrInputUncertain):
		detail.Kind, detail.ExitCode = "control_uncertain", ExitAmbiguous
	case errors.Is(err, setupcore.ErrSetupConflict), errors.Is(err, setupcore.ErrMigrationNeeded), errors.Is(err, setupcore.ErrRollbackConflict):
		detail.Kind, detail.ExitCode = "setup_conflict", ExitConflict
	case errors.Is(err, setupcore.ErrHostUnavailable):
		detail.Kind, detail.ExitCode = "host_unavailable", ExitUnavailable
	case errors.Is(err, setupcore.ErrCommandFailed), errors.Is(err, setupcore.ErrVerification):
		detail.Kind, detail.ExitCode = "setup_verification_failed", ExitUnavailable
	case errors.Is(err, setupcore.ErrReceiptNotFound):
		detail.Kind, detail.ExitCode = "setup_not_installed", ExitNotFound
	case errors.Is(err, updatecore.ErrUnsupportedOwner):
		detail.Kind, detail.ExitCode = string(updatecore.ErrUnsupportedOwner), ExitUnsupported
	case errors.Is(err, updatecore.ErrInvalidRequest):
		detail.Kind, detail.ExitCode = string(updatecore.ErrInvalidRequest), ExitUsage
	case errors.Is(err, updatecore.ErrInvalidReceipt), errors.Is(err, updatecore.ErrReceiptMismatch):
		detail.Kind, detail.ExitCode = "install_receipt_invalid", ExitConflict
	case errors.Is(err, updatecore.ErrReleaseNotFound):
		detail.Kind, detail.ExitCode = string(updatecore.ErrReleaseNotFound), ExitNotFound
	case errors.Is(err, updatecore.ErrRollbackUnavailable):
		detail.Kind, detail.ExitCode = string(updatecore.ErrRollbackUnavailable), ExitNotFound
	case errors.Is(err, context.Canceled):
		detail.Kind, detail.ExitCode = "canceled", ExitUnavailable
	case errors.Is(err, updatecore.ErrReleaseResolution):
		detail.Kind, detail.ExitCode, detail.Retryable = string(updatecore.ErrReleaseResolution), ExitUnavailable, true
	case errors.Is(err, updatecore.ErrUpdateInProgress):
		detail.Kind, detail.ExitCode, detail.Retryable = string(updatecore.ErrUpdateInProgress), ExitConflict, true
	case errors.Is(err, context.DeadlineExceeded):
		detail.Kind, detail.ExitCode, detail.Retryable = "unavailable", ExitUnavailable, true
	}
	return detail
}

func safeErrorMessage(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

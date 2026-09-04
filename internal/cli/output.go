package cli

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/kaaanata/jetkvm-cli/internal/control"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	"github.com/kaaanata/jetkvm-cli/internal/input"
	"github.com/kaaanata/jetkvm-cli/internal/operation"
	setupcore "github.com/kaaanata/jetkvm-cli/internal/setup"
	updatecore "github.com/kaaanata/jetkvm-cli/internal/update"
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

func (a *App) writeResult(command string, data any, textWriter func(io.Writer) error) error {
	mode, err := a.resolvedOutputMode()
	if err != nil {
		return err
	}
	if mode == "text" {
		return textWriter(a.deps.Stdout)
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

func renderFailure(w io.Writer, mode string, err error) error {
	detail := classifyFailure(err)
	if mode == "json" {
		return writeJSON(w, failureEnvelope{SchemaVersion: "v1", Error: detail})
	}
	_, writeErr := fmt.Fprintf(w, "Error [%s]: %s\n", detail.Kind, detail.Message)
	return writeErr
}

// ExitCode maps command failures to the stable process exit contract.
func ExitCode(err error) int { return classifyFailure(err).ExitCode }

func classifyFailure(err error) failureDetail {
	detail := failureDetail{Kind: "internal", Message: safeErrorMessage(err), ExitCode: ExitInternal}
	_, isUsage := errors.AsType[*usageFailure](err)
	switch {
	case isUsage:
		detail.Kind, detail.ExitCode = "invalid_argument", ExitUsage
	case errors.Is(err, domain.ErrDeviceNotExposed):
		detail.Kind, detail.ExitCode = "device_not_exposed", ExitNotFound
	case errors.Is(err, domain.ErrDeviceIdentityMismatch):
		detail.Kind, detail.ExitCode = "device_identity_mismatch", ExitNotFound
	case errors.Is(err, domain.ErrCapabilityUnavailable):
		detail.Kind, detail.ExitCode = "capability_unavailable", ExitUnsupported
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
	case errors.Is(err, control.ErrGenerationMismatch), errors.Is(err, input.ErrStaleGeneration):
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
	case errors.Is(err, updatecore.ErrReleaseResolution):
		detail.Kind, detail.ExitCode, detail.Retryable = string(updatecore.ErrReleaseResolution), ExitUnavailable, true
	case errors.Is(err, updatecore.ErrSignatureVerification), errors.Is(err, updatecore.ErrChecksumMismatch):
		detail.Kind, detail.ExitCode = "update_verification_failed", ExitAuth
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

func writeStatusText(w io.Writer, status domain.DeviceStatus) error {
	if _, err := fmt.Fprintf(w, "%s (%s)\n", status.Alias, status.DeviceID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "reachable: %t\nobserved: %s\n", status.Reachable, status.Observed.Format("2006-01-02T15:04:05Z07:00")); err != nil {
		return err
	}
	keys := make([]string, 0, len(status.Fields))
	for key := range status.Fields {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		field := status.Fields[key]
		value := field.Value
		if field.Unavailable != "" {
			value = "unavailable: " + field.Unavailable
		}
		if _, err := fmt.Fprintf(w, "%s: %v [%s]\n", key, value, field.Source); err != nil {
			return err
		}
	}
	return nil
}

func writeCapabilitiesText(w io.Writer, snapshot domain.CapabilitySnapshot) error {
	if _, err := fmt.Fprintf(w, "%s (%s)\n", snapshot.Alias, snapshot.DeviceID); err != nil {
		return err
	}
	for _, item := range snapshot.Items {
		state := "unavailable"
		if item.Compiled && item.Configured && item.FirmwareSupported && item.CurrentlyReady {
			state = "ready"
		}
		if item.Reason != "" {
			state += ": " + item.Reason
		}
		if _, err := fmt.Fprintf(w, "%s: %s\n", item.Name, state); err != nil {
			return err
		}
	}
	return nil
}

func writeDoctorText(w io.Writer, report doctorReport) error {
	state := "healthy"
	if !report.Healthy {
		state = "attention required"
	}
	if _, err := fmt.Fprintf(w, "doctor: %s\n", state); err != nil {
		return err
	}
	if err := writeStatusText(w, report.Status); err != nil {
		return err
	}
	if err := writeCapabilitiesText(w, report.Capabilities); err != nil {
		return err
	}
	for _, warning := range report.Warnings {
		if _, err := fmt.Fprintf(w, "warning: %s\n", strings.TrimSpace(warning)); err != nil {
			return err
		}
	}
	return nil
}

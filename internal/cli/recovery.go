package cli

import "github.com/kaaanata/jetkvm-cli/internal/terminal"

// Human detail filtering never changes machine receipts or hides safety facts.
func compactDocument(document terminal.Document) terminal.Document {
	for i := range document.Sections {
		section := &document.Sections[i]
		if len(section.Headers) != 0 {
			continue
		}
		var rows [][]string
		for _, row := range section.Rows {
			if len(row) == 0 {
				continue
			}
			if row[0] == "terminal claim" && len(row) > 1 && row[1] == document.Title {
				continue
			}
			switch row[0] {
			case "operation", "handle", "generation", "observation", "receipt", "captured", "observed", "idle expires", "absolute expires":
				continue
			}
			rows = append(rows, row)
		}
		section.Rows = rows
	}
	return document
}

func recoveryHint(kind string) string {
	switch kind {
	case "install_receipt_invalid", "unsupported_install_owner":
		return "Use the installer that owns this executable. Do not overwrite or invent an ownership receipt."
	case "update_in_progress":
		return "Let the current update finish, then run jetkvm update --check."
	case "update_verification_failed":
		return "Do not bypass verification. Check the official release and your network before trying again."
	case "rollback_failed":
		return "Do not rerun the update blindly. Inspect the executable and install receipts; recover using the original installer."
	case "update_apply_failed":
		return "Read the reported restoration outcome, then inspect the installation before attempting another update."
	case "release_resolution_failed":
		return "Check GitHub connectivity, then run jetkvm update --check."
	case "device_not_exposed":
		return "Run jetkvm devices list and select an explicitly configured device."
	case "device_identity_mismatch":
		return "Check the configured device route and identity. Do not silently replace its pin."
	case "takeover_disabled", "confirmation_required", "confirmation_unavailable":
		return "Review the configured action policy. Required device-action approval cannot be bypassed."
	case "observation_stale":
		return "Capture a fresh screen before choosing coordinates. Inspect any retained input receipt before repeating an action."
	case "control_uncertain", "operation_conflict":
		return "Inspect the device and retained operation receipt. Do not automatically repeat the input or power action."
	case "canceled":
		return "Review any partial result before retrying; cancellation does not undo an already delivered action."
	case "unavailable":
		return "Check device or network availability. Inspect any partial result before repeating a state-changing action."
	case "setup_conflict":
		return "Run jetkvm setup doctor and resolve the reported ownership conflict."
	}
	return ""
}

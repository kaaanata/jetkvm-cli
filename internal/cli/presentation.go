package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/kaaanata/jetkvm-cli/internal/automation"
	"github.com/kaaanata/jetkvm-cli/internal/buildinfo"
	"github.com/kaaanata/jetkvm-cli/internal/domain"
	setupcore "github.com/kaaanata/jetkvm-cli/internal/setup"
	"github.com/kaaanata/jetkvm-cli/internal/terminal"
	updatecore "github.com/kaaanata/jetkvm-cli/internal/update"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// resultDocument is the human projection of typed receipts. Machine encoding
// bypasses this layer entirely; unknown result types must add an explicit view.
func resultDocument(command string, data any) (terminal.Document, error) {
	d := terminal.Document{Title: "jetkvm " + strings.ReplaceAll(command, ".", " ")}
	row, fields := terminal.Row, terminal.Fields
	switch v := data.(type) {
	case buildinfo.Info:
		d.Title = "jetkvm " + v.Version
		d.Sections = append(d.Sections, fields("Build", row("commit", v.Commit), row("built", v.Date), row("runtime", v.Go+" "+v.OS+"/"+v.Arch)))
	case deviceListResult:
		d.Title = "Configured devices"
		s := terminal.Section{Headers: []string{"Alias", "Device ID", "Origin"}}
		for _, device := range v.Devices {
			id := string(device.ID)
			if id == "" {
				id = "unverified"
			}
			s.Rows = append(s.Rows, []string{device.Alias, id, fmt.Sprint(device.Origin)})
		}
		if len(s.Rows) == 0 {
			s.Text = "No configured devices."
		}
		d.Sections = append(d.Sections, s)
	case domain.DeviceStatus:
		d.Title, d.Tone = "Device is unreachable", "attention"
		if v.Reachable {
			d.Title, d.Tone = "Device is reachable", "success"
		}
		d.Sections = append(d.Sections, statusSections(v)...)
	case domain.CapabilitySnapshot:
		d.Title = "Device capabilities"
		d.Sections = append(d.Sections, capabilitySection(v))
	case doctorReport:
		d.Title, d.Tone = "Device checks passed", "success"
		if !v.Healthy {
			d.Title, d.Tone = "Device needs attention", "attention"
		}
		d.Sections = append(d.Sections, statusSections(v.Status)...)
		d.Sections = append(d.Sections, capabilitySection(v.Capabilities))
		for _, warning := range v.Warnings {
			d.Sections = append(d.Sections, terminal.Section{Title: "Warning", Text: warning})
		}
	case controlHandleResult:
		d.Sections = append(d.Sections, controlSection(v))
	case controlSnapshotResult:
		d.Sections = append(d.Sections, fields("Connection", row("transport", v.Transport), row("session", v.Session)))
		if v.Handle != nil {
			d.Sections = append(d.Sections, controlSection(*v.Handle))
		}
	case operationReceiptResult:
		d.Title = "Operation receipt"
		if v.TerminalClaim != "" {
			d.Title = v.TerminalClaim
		}
		d.Sections = append(d.Sections, operationSections(v)...)
	case runActionsResult:
		d.Title = "Input batch receipt"
		if v.Operation.TerminalClaim != "" {
			d.Title = v.Operation.TerminalClaim
		}
		d.Sections = append(d.Sections, operationSections(v.Operation)...)
		d.Sections = append(d.Sections, fields("Input batch", row("status", v.Batch.Status), row("neutralized", v.Batch.Neutralized), row("actions", len(v.Batch.Actions)), row("cleanup failure", v.Batch.CleanupFailure)))
		if v.Observation != nil {
			d.Sections = append(d.Sections, screenshotSection(*v.Observation))
		}
	case automation.PowerState:
		d.Title = "Power state observed"
		d.Sections = append(d.Sections, fields("Power", row("device", v.DeviceID), row("extension", v.ActiveExtension), row("power LED", v.PowerLED), row("HDD LED", v.HDDLED), row("observed", v.ObservedAt.Format(time.RFC3339Nano))))
	case *screenshotResult:
		d.Title = "Screen captured"
		if v.File != "" {
			d.Title = "Screenshot saved"
		}
		d.Sections = append(d.Sections, screenshotSection(*v))
	case []setupcore.Plan:
		d.Title = "Agent setup plan"
		for _, plan := range v {
			s := fields(string(plan.Target.Host), row("state", plan.InitialState), row("mode", plan.Target.Mode), row("scope", plan.Target.Scope), row("steps", len(plan.Steps)))
			for _, step := range plan.Steps {
				s.Rows = append(s.Rows, row("step", step.Name))
			}
			d.Sections = append(d.Sections, s)
		}
	case []setupcore.Receipt:
		d.Title = "Agent setup results"
		for _, receipt := range v {
			d.Sections = append(d.Sections, setupReceiptSection(receipt))
		}
	case setupcore.Receipt:
		d.Title = "Agent setup result"
		d.Sections = append(d.Sections, setupReceiptSection(v))
	case []setupcore.DoctorReport:
		d.Title = "Agent integration checks"
		for _, report := range v {
			d.Sections = append(d.Sections, fields(string(report.Target.Host), row("status", report.Status), row("state", report.State)))
			s := terminal.Section{Headers: []string{"Check", "Result", "Details"}}
			for _, check := range report.Checks {
				state := "needs attention"
				if check.OK {
					state = "passed"
				}
				s.Rows = append(s.Rows, []string{check.Name, state, check.Message})
			}
			d.Sections = append(d.Sections, s)
		}
		if len(v) == 0 {
			d.Sections = append(d.Sections, terminal.Section{Text: "No available agent hosts."})
		}
	case updatecore.CheckResult:
		d.Title, d.Tone = "Already up to date — JetKVM "+v.Installation.Version+".", "success"
		if v.Available {
			d.Title, d.Tone = "JetKVM "+v.Release.Version+" is available", ""
			next := "jetkvm update"
			if v.Installation.Owner != updatecore.OwnerStandalone {
				next = "Use the installation owner's update mechanism."
			}
			d.Sections = append(d.Sections, fields("", row("installed", v.Installation.Version), row("owner", v.Installation.Owner)), terminal.Section{Title: "Next", Text: next})
		}
	case updatecore.Plan:
		d.Title = "Update plan"
		if v.Action == updatecore.ActionNone {
			d.Title, d.Tone = "Already up to date — JetKVM "+v.CurrentVersion+".", "success"
			break
		}
		if v.Action == updatecore.ActionRequired {
			d.Title, d.Tone = "Update through your installer", "attention"
		}
		d.Sections = append(d.Sections, terminal.Section{Text: v.CurrentVersion + " → " + v.TargetVersion}, fields("", row("owner", v.Owner), row("executable", v.Executable)))
		if len(v.Command) > 0 {
			d.Sections = append(d.Sections, terminal.Section{Title: "Run with your installer", Text: strings.Join(v.Command, " ")})
		}
	case updatecore.Result:
		return updateDocument(v), nil
	default:
		return d, fmt.Errorf("human presentation is not defined for %s (%T)", command, data)
	}
	return d, nil
}

func updateDocument(v updatecore.Result) terminal.Document {
	d := terminal.Document{}
	switch v.Status {
	case updatecore.StatusUpToDate:
		d.Title, d.Tone = "Already up to date — JetKVM "+v.CurrentVersion+".", "success"
		return d
	case updatecore.StatusActionRequired:
		d.Title, d.Tone = "Update through your installer", "attention"
		d.Sections = append(d.Sections, terminal.Fields("", terminal.Row("installed", v.CurrentVersion), terminal.Row("owner", v.Owner)))
		if len(v.ActionRequired) > 0 {
			d.Sections = append(d.Sections, terminal.Section{Title: "Run with your installer", Text: strings.Join(v.ActionRequired, " ")})
		} else {
			d.Sections = append(d.Sections, terminal.Section{Text: "Follow the update instructions for your installation."})
		}
		return d
	case updatecore.StatusApplied:
		d.Title, d.Tone = "JetKVM updated", "success"
	case updatecore.StatusRolledBack:
		d.Title, d.Tone = "JetKVM rolled back", "success"
	default:
		d.Title, d.Tone = "Update outcome needs attention", "attention"
		d.Sections = append(d.Sections, terminal.Fields("", terminal.Row("status", v.Status)))
	}
	version := v.CurrentVersion
	if v.PreviousVersion != "" {
		version = v.PreviousVersion + " → " + v.CurrentVersion
	}
	d.Sections = append(d.Sections, terminal.Section{Text: version})
	verification := "Artifact verification not recorded"
	if v.Verified {
		verification = "Artifact verified"
	}
	if v.RollbackAvailable {
		verification += " · rollback available"
	}
	d.Sections = append(d.Sections, terminal.Section{Text: verification})
	if v.RollbackAvailable {
		d.Sections = append(d.Sections, terminal.Section{Title: "Undo", Text: "jetkvm update rollback"})
	}
	return d
}

func statusSections(v domain.DeviceStatus) []terminal.Section {
	row := terminal.Row
	details := terminal.Fields(v.Alias, row("device", v.DeviceID), row("reachable", v.Reachable), row("observed", v.Observed.Format(time.RFC3339Nano)))
	s := terminal.Section{Headers: []string{"Field", "Value", "Source"}}
	keys := make([]string, 0, len(v.Fields))
	for key := range v.Fields {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		field := v.Fields[key]
		value := fmt.Sprint(field.Value)
		if field.Unavailable != "" {
			value = "unavailable: " + field.Unavailable
		}
		s.Rows = append(s.Rows, []string{key, value, fmt.Sprint(field.Source)})
	}
	return []terminal.Section{details, s}
}

func capabilitySection(v domain.CapabilitySnapshot) terminal.Section {
	s := terminal.Section{Title: v.Alias + " (" + string(v.DeviceID) + ")", Headers: []string{"Capability", "State", "Reason"}}
	for _, item := range v.Items {
		state := "unavailable"
		if item.Compiled && item.Configured && item.FirmwareSupported && item.CurrentlyReady {
			state = "ready"
		}
		s.Rows = append(s.Rows, []string{fmt.Sprint(item.Name), state, item.Reason})
	}
	return s
}

func controlSection(v controlHandleResult) terminal.Section {
	row := terminal.Row
	return terminal.Fields("Control", row("handle", v.HandleID), row("device", v.DeviceID), row("generation", v.Generation), row("state", v.State), row("capabilities", strings.Join(v.Capabilities, ", ")), row("idle expires", v.IdleExpiresAt.Format(time.RFC3339Nano)), row("absolute expires", v.AbsoluteExpiresAt.Format(time.RFC3339Nano)))
}

func operationSections(v operationReceiptResult) []terminal.Section {
	row := terminal.Row
	sections := []terminal.Section{terminal.Fields("Operation receipt", row("operation", v.OperationID), row("device", v.DeviceID), row("action", v.Action), row("stage", v.Stage), row("delivery", v.Delivery), row("verified", v.Verification), row("terminal claim", v.TerminalClaim), row("retry safe", v.RetrySafe))}
	if v.ErrorKind != "" {
		sections = append(sections, terminal.Fields("Failure", row("kind", v.ErrorKind)))
	}
	for _, warning := range v.Warnings {
		sections = append(sections, terminal.Section{Title: "Warning", Text: warning})
	}
	return sections
}

func screenshotSection(v screenshotResult) terminal.Section {
	row := terminal.Row
	section := terminal.Fields("Screenshot", row("file", v.File), row("observation", v.Observation.ID), row("device", v.Observation.DeviceID), row("captured", v.Observation.CapturedAt.Format(time.RFC3339Nano)), row("trust", v.Observation.Trust))
	if frame := v.Observation.Frame; frame.Width > 0 && frame.Height > 0 {
		section.Rows = append(section.Rows, row("size", fmt.Sprintf("%d × %d", frame.Width, frame.Height)))
	}
	return section
}

func setupReceiptSection(v setupcore.Receipt) terminal.Section {
	row := terminal.Row
	outcome := string(v.Status)
	switch v.Status {
	case setupcore.ReceiptCommitted:
		outcome = "Setup completed"
	case setupcore.ReceiptPrepared:
		outcome = "Setup prepared"
	case setupcore.ReceiptDryRun:
		outcome = "Preview only"
	case setupcore.ReceiptUninstalled:
		outcome = "Integration removed"
	case setupcore.ReceiptRolledBack:
		outcome = "Setup rolled back"
	case setupcore.ReceiptRollbackConflict:
		outcome = "Rollback conflict — needs attention"
	case setupcore.ReceiptFailed:
		outcome = "Setup failed"
	}
	s := terminal.Fields(string(v.Target.Host)+" · "+outcome, row("mode", v.Target.Mode), row("scope", v.Target.Scope), row("receipt", v.ID), row("owned components", strings.Join(v.OwnedComponents, ", ")))
	if v.FailureKind != "" {
		s.Rows = append(s.Rows, row("failure", v.FailureKind))
	}
	return s
}

// Cobra remains the command/flag authority, including completion. Help reads
// its live metadata rather than maintaining a separate command catalogue.
func (a *App) writeHelp(cmd *cobra.Command, output io.Writer) error {
	cmd.InitDefaultHelpFlag()
	d := terminal.Document{Title: cmd.CommandPath(), Sections: []terminal.Section{{Text: cmd.Short}}}
	if cmd.Long != "" {
		d.Sections[0].Text = cmd.Long
	}
	usage := cmd.UseLine()
	if cmd.HasAvailableSubCommands() && !strings.Contains(usage, "[command]") {
		usage += " [command]"
	}
	d.Sections = append(d.Sections, terminal.Section{Title: "Usage", Text: usage})
	if len(cmd.Aliases) > 0 {
		d.Sections = append(d.Sections, terminal.Section{Title: "Aliases", Text: strings.Join(cmd.Aliases, ", ")})
	}
	if cmd.Example != "" {
		d.Sections = append(d.Sections, terminal.Section{Title: "Examples", Text: cmd.Example})
	}
	commands := terminal.Section{Title: "Commands"}
	groups := []terminal.Section{{Title: "Inspect"}, {Title: "Control"}, {Title: "Integrate"}, {Title: "Maintain"}, {Title: "More commands"}}
	for _, child := range cmd.Commands() {
		if child.IsAvailableCommand() || child.Name() == "help" {
			commands.Rows = append(commands.Rows, []string{child.Name(), child.Short})
			group := 4
			switch child.Name() {
			case "devices", "status", "doctor", "capabilities":
				group = 0
			case "screenshot", "input", "power", "control":
				group = 1
			case "setup", "mcp", "cloud":
				group = 2
			case "update", "version", "completion", "help":
				group = 3
			}
			groups[group].Rows = append(groups[group].Rows, []string{child.Name(), child.Short})
		}
	}
	if len(commands.Rows) > 0 {
		if cmd.Parent() == nil {
			d.Title = "JETKVM"
			if a.deps.Version.Version != "" {
				d.Title += "  " + a.deps.Version.Version
			}
			if cmd.Example == "" {
				var examples []string
				for _, child := range cmd.Commands() {
					if child.Name() == "devices" && child.IsAvailableCommand() {
						examples = append(examples, cmd.CommandPath()+" devices list")
					}
					if child.Name() == "screenshot" && child.IsAvailableCommand() {
						examples = append(examples, cmd.CommandPath()+" screenshot <device> --file screen.png")
					}
				}
				if len(examples) > 0 {
					d.Sections = append(d.Sections, terminal.Section{Title: "Get started", Text: strings.Join(examples, "\n")})
				}
			}
			for _, group := range groups {
				if len(group.Rows) > 0 {
					d.Sections = append(d.Sections, group)
				}
			}
		} else {
			d.Sections = append(d.Sections, commands)
		}
	}
	flags := func(title string, set *pflag.FlagSet) {
		s := terminal.Section{Title: title}
		set.VisitAll(func(f *pflag.Flag) {
			if f.Hidden {
				return
			}
			name := "--" + f.Name
			if f.Shorthand != "" && f.ShorthandDeprecated == "" {
				name = "-" + f.Shorthand + ", " + name
			}
			value, usage := pflag.UnquoteUsage(f)
			if value != "" {
				name += " " + value
			}
			if f.DefValue != "" && f.DefValue != "false" {
				usage += " (default: " + f.DefValue + ")"
			}
			if f.Deprecated != "" {
				usage += " (deprecated: " + f.Deprecated + ")"
			}
			s.Rows = append(s.Rows, []string{name, usage})
		})
		if len(s.Rows) > 0 {
			d.Sections = append(d.Sections, s)
		}
	}
	flags("Flags", cmd.LocalFlags())
	flags("Global flags", cmd.InheritedFlags())
	if len(commands.Rows) > 0 {
		d.Sections = append(d.Sections, terminal.Section{Text: "Use \"" + cmd.CommandPath() + " [command] --help\" for more information."})
	}
	return terminal.New(output, a.deps.IsTerminal(output)).Write(d)
}

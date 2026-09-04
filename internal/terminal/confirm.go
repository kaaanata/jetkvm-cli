package terminal

import (
	"context"
	"errors"
	"io"
	"os"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/cancelreader"
)

// Confirm collects a choice only. Callers must still check their policy and
// mint a proof bound to the authoritative request after an affirmative result.
func (r Renderer) Confirm(ctx context.Context, input io.Reader, title, description string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, context.Cause(ctx)
	}
	approved := false
	field := huh.NewConfirm().Title(Clean(title)).Description(Clean(description)).
		Affirmative("Confirm").Negative("Cancel").Value(&approved)
	theme := huh.ThemeFunc(func(dark bool) *huh.Styles {
		s := huh.ThemeBase(dark)
		s.Focused.Title = r.Theme.Title
		s.Focused.Description = r.Theme.Muted
		s.Focused.FocusedButton = r.Theme.Label.Reverse(true).Padding(0, 1)
		s.Focused.BlurredButton = lipgloss.NewStyle().Padding(0, 1)
		s.Focused.ErrorMessage = r.Theme.Error
		s.Blurred = s.Focused
		return s
	})
	form := huh.NewForm(huh.NewGroup(field)).WithTheme(theme).WithWidth(max(1, min(r.Width, 88))).
		WithInput(input).WithOutput(r.Output)
	accessible := !r.Styled || !IsTerminal(input) || os.Getenv("JETKVM_ACCESSIBLE") == "1"
	if !accessible {
		err := runConfirmationForm(ctx, form, input, r.Output)
		if ctx.Err() != nil {
			return false, context.Cause(ctx)
		}
		_, writeErr := io.WriteString(r.Output, "\n")
		err = errors.Join(err, writeErr)
		return approved && err == nil, err
	}
	// Huh's accessible runner currently ignores context and I/O errors. Supply
	// cancellable input and record both stream errors, then join before deciding.
	reader, err := cancelreader.NewReader(input)
	if err != nil {
		return false, err
	}
	defer reader.Close()
	canceled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { reader.Cancel(); close(canceled) })
	defer func() {
		if !stop() {
			<-canceled
		}
	}()
	in := &checkedReader{Reader: reader}
	out := &plainPromptWriter{output: r.Output, cancel: reader.Cancel}
	// Huh's accessible confirm displays its title but omits its description.
	// Present context first so device identity is visible in every mode.
	if description != "" {
		if err := (Renderer{Output: out, Width: r.Width, Theme: r.Theme}).Write(Document{Sections: []Section{{Text: description}}}); err != nil {
			return false, err
		}
	}
	err = form.WithAccessible(true).WithInput(in).WithOutput(out).RunWithContext(ctx)
	if ctx.Err() != nil {
		return false, context.Cause(ctx)
	}
	err = errors.Join(err, in.err, out.err)
	return approved && err == nil, err
}

// Huh owns the fields, navigation and view. The CLI owns cancellation: a
// canceled operation requests graceful UI exit, then joins Program.Run before
// returning the cause. Passing that context directly to Bubble Tea selects
// its kill path, which skips joining the input reader before closing it.
func runConfirmationForm(ctx context.Context, form *huh.Form, input io.Reader, output io.Writer) error {
	form.SubmitCmd = tea.Quit
	form.CancelCmd = tea.Quit
	program := tea.NewProgram(confirmationForm{form}, tea.WithInput(input), tea.WithOutput(output),
		tea.WithContext(context.WithoutCancel(ctx)), tea.WithoutSignalHandler())
	// The executable's signal context is the single cancellation authority.
	// Send is safe even if the program completes concurrently with cancellation.
	joined := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { program.Send(tea.QuitMsg{}); close(joined) })
	_, err := program.Run()
	if !stop() {
		<-joined
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if form.State == huh.StateAborted {
		return huh.ErrUserAborted
	}
	return err
}

// Adapt Huh's string View to Bubble Tea v2 without changing its form model.
type confirmationForm struct{ *huh.Form }

func (f confirmationForm) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, command := f.Form.Update(msg)
	f.Form = model.(*huh.Form)
	return f, command
}

func (f confirmationForm) View() tea.View {
	view := tea.NewView(f.Form.View())
	view.ReportFocus = true
	return view
}

type checkedReader struct {
	io.Reader
	err error
}

func (r *checkedReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		r.err = err
	}
	return n, err
}

type plainPromptWriter struct {
	output io.Writer
	err    error
	cancel func() bool
}

func (w *plainPromptWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	clean := ansi.Strip(string(p))
	n, err := io.WriteString(w.output, clean)
	if err == nil && n != len(clean) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.err = err
		w.cancel()
		return 0, err
	}
	return len(p), nil
}

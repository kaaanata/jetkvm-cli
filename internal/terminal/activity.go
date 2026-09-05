package terminal

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	events "github.com/kaaanata/jetkvm-cli/internal/progress"
)

// Activity owns transient stderr rendering. The command owns cancellation and
// calls Close only after business work and cleanup have joined. No input reader,
// alternate screen, business goroutine, or competing cancellation owner exists.
type Activity struct {
	mu               sync.Mutex
	terminalMu       sync.Mutex
	output           *activityOutput
	renderer         Renderer
	event            events.Event
	started, changed time.Time
	stageStarted     time.Time
	program          *tea.Program
	done             chan struct{}
	err              error
	paused, closed   bool
}

func NewActivity(output io.Writer, tty bool) *Activity {
	r := New(output, tty)
	w := &activityOutput{output: output}
	r.Output = w
	return &Activity{renderer: r, output: w}
}

func (a *Activity) Report(event events.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	event.Stage = strings.ReplaceAll(Clean(event.Stage), "\n", " ")
	if event.Stage == "" {
		return
	}
	now := time.Now()
	if a.started.IsZero() {
		a.started = now
	}
	stageChanged := event.Stage != a.event.Stage
	if stageChanged || event.Completed < a.event.Completed {
		a.stageStarted = now
	}
	if stageChanged || event.Completed != a.event.Completed {
		a.changed = now
	}
	a.event = event
	if a.paused {
		return
	}
	if !a.renderer.Styled {
		if stageChanged {
			_, err := fmt.Fprintln(a.renderer.Output, event.Stage+"…")
			if a.err == nil {
				a.err = err
			}
		}
		return
	}
	if a.program == nil {
		a.startLocked()
	}
}

func (a *Activity) startLocked() {
	done := make(chan struct{})
	model := activityModel{activity: a, width: a.renderer.Width,
		spinner: spinner.New(spinner.WithSpinner(spinner.Line), spinner.WithStyle(a.renderer.Theme.Title)),
		bar:     progress.New(progress.WithoutPercentage(), progress.WithColors(lipgloss.Color("6"))),
	}
	p := tea.NewProgram(model, tea.WithInput(nil), tea.WithOutput(a.renderer.Output), tea.WithWindowSize(a.renderer.Width, 24), tea.WithoutSignalHandler())
	a.program, a.done = p, done
	go func() {
		_, err := p.Run()
		a.mu.Lock()
		if a.err == nil {
			a.err = err
		}
		a.mu.Unlock()
		close(done)
	}()
}

func (a *Activity) Stage() string { a.mu.Lock(); defer a.mu.Unlock(); return a.event.Stage }

// Pause joins the renderer before a prompt takes ownership of the terminal.
func (a *Activity) Pause() {
	a.terminalMu.Lock()
	defer a.terminalMu.Unlock()
	a.mu.Lock()
	a.paused = true
	p, done := a.program, a.done
	a.mu.Unlock()
	if p != nil {
		p.Quit()
		<-done
	}
	a.mu.Lock()
	a.program, a.done = nil, nil
	a.mu.Unlock()
}

func (a *Activity) Resume() { a.mu.Lock(); a.paused = false; a.mu.Unlock() }

func (a *Activity) Close() error {
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
	a.Pause()
	a.mu.Lock()
	defer a.mu.Unlock()
	return errors.Join(a.err, a.output.Err())
}

// Write lets the existing logger print above the live view. It never competes
// with Bubble Tea's writes to the underlying terminal.
func (a *Activity) Write(data []byte) (int, error) {
	a.terminalMu.Lock()
	defer a.terminalMu.Unlock()
	a.mu.Lock()
	p := a.program
	if p == nil {
		_, err := io.WriteString(a.renderer.Output, Clean(string(data)))
		a.mu.Unlock()
		if err != nil {
			return 0, err
		}
		return len(data), nil
	}
	a.mu.Unlock()
	p.Println(strings.TrimSuffix(Clean(string(data)), "\n"))
	return len(data), nil
}

type activityOutput struct {
	mu     sync.Mutex
	output io.Writer
	err    error
}

func (w *activityOutput) Fd() uintptr {
	if f, ok := w.output.(interface{ Fd() uintptr }); ok {
		return f.Fd()
	}
	return ^uintptr(0)
}

// Preserve the borrowed terminal's file interface so Bubble Tea observes its
// real dimensions and resize signals. This adapter never owns or closes stderr.
func (w *activityOutput) Read(p []byte) (int, error) {
	if reader, ok := w.output.(io.Reader); ok {
		return reader.Read(p)
	}
	return 0, io.EOF
}
func (*activityOutput) Close() error { return nil }
func (w *activityOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	// Bubble Tea 2.0.9 suppresses keyboard probes with WithInput(nil), but
	// still emits these two startup capability queries from Program.Run.
	// An output-only surface cannot consume their replies: letting them pass
	// would put protocol bytes into the user's next shell/prompt input. Keep
	// this narrow output contract until upstream's no-input guard covers them.
	filtered := bytes.ReplaceAll(p, []byte(ansi.RequestModeSynchronizedOutput), nil)
	filtered = bytes.ReplaceAll(filtered, []byte(ansi.RequestModeUnicodeCore), nil)
	n, err := w.output.Write(filtered)
	if err == nil && n != len(filtered) {
		err = io.ErrShortWrite
	}
	w.err = err
	if err != nil {
		return 0, err
	}
	return len(p), nil
}
func (w *activityOutput) Err() error { w.mu.Lock(); defer w.mu.Unlock(); return w.err }

type activityModel struct {
	activity *Activity
	width    int
	spinner  spinner.Model
	bar      progress.Model
}

func (m activityModel) Init() tea.Cmd { return m.spinner.Tick }
func (m activityModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = max(1, size.Width)
	}
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	return m, cmd
}
func (m activityModel) View() tea.View {
	a := m.activity
	a.mu.Lock()
	event, started, changed, transferStarted, hidden := a.event, a.started, a.changed, a.stageStarted, a.paused || a.closed
	a.mu.Unlock()
	if hidden || time.Since(started) < 150*time.Millisecond {
		view := tea.NewView("")
		view.DisableBracketedPasteMode = true
		return view
	}
	label := fmt.Sprintf("%s %s  %s", m.spinner.View(), event.Stage, time.Since(started).Truncate(time.Second))
	if event.Total > 0 {
		ratio := min(1, max(0, float64(event.Completed)/float64(event.Total)))
		m.bar.SetWidth(max(1, min(32, m.width-8)))
		label += fmt.Sprintf("\n%s %3d%%\n%.1f / %.1f MiB", m.bar.ViewAs(ratio), int(ratio*100), float64(event.Completed)/(1<<20), float64(event.Total)/(1<<20))
	} else if event.Completed > 0 {
		label += fmt.Sprintf("\n%.1f MiB received", float64(event.Completed)/(1<<20))
	}
	if event.Completed > 0 && time.Since(transferStarted) > 200*time.Millisecond {
		label += fmt.Sprintf(" · %.1f MiB/s", float64(event.Completed)/(1<<20)/time.Since(transferStarted).Seconds())
	}
	if time.Since(changed) >= 10*time.Second {
		label += "\nStill waiting; no new progress reported."
	}
	view := tea.NewView(lipgloss.NewStyle().Width(max(1, m.width)).Render(label))
	view.DisableBracketedPasteMode = true
	return view
}

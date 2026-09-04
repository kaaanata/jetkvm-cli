// Package terminal owns human presentation only. It never authorizes an action
// or writes to a machine protocol stream.
package terminal

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// Theme keeps results, help, errors and forms visually consistent. ANSI colors
// adapt to the user's terminal palette; meaning never depends on color alone.
type Theme struct {
	Title, Label, Muted, Error lipgloss.Style
}

func NewTheme() Theme {
	return Theme{
		Title: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
		Label: lipgloss.NewStyle().Bold(true),
		Muted: lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		Error: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1")),
	}
}

// Section is semantic presentation data, independent of terminal escape codes.
type Section struct {
	Title   string
	Text    string
	Headers []string
	Rows    [][]string
}

type Document struct {
	Title    string
	Sections []Section
	Failure  bool
}

type Renderer struct {
	Output io.Writer
	Width  int
	Styled bool
	Theme  Theme
}

func IsTerminal(stream any) bool {
	f, ok := stream.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(f.Fd())
}

func New(output io.Writer, tty bool) Renderer {
	width := 80
	if f, ok := output.(interface{ Fd() uintptr }); ok && tty {
		if w, _, err := term.GetSize(f.Fd()); err == nil && w > 0 {
			width = w
		}
	}
	return Renderer{Output: output, Width: width,
		Styled: tty && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" && os.Getenv("JETKVM_ACCESSIBLE") != "1",
		Theme:  NewTheme()}
}

// Clean removes terminal commands and non-printing control characters from
// device names, errors and other untrusted display text. JSON is never cleaned.
func Clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(s))
}

func (r Renderer) Render(d Document) string {
	width := max(1, r.Width)
	style := r.Theme.Title
	if d.Failure {
		style = r.Theme.Error
	}
	var blocks []string
	if d.Title != "" {
		blocks = append(blocks, style.Width(width).Render(Clean(d.Title)))
	}
	for _, section := range d.Sections {
		var parts []string
		if section.Title != "" {
			parts = append(parts, r.Theme.Label.Width(width).Render(Clean(section.Title)))
		}
		if section.Text != "" {
			parts = append(parts, lipgloss.NewStyle().Width(width).Render(Clean(section.Text)))
		}
		if len(section.Rows) > 0 {
			rows := make([][]string, len(section.Rows))
			for i, row := range section.Rows {
				for _, cell := range row {
					rows[i] = append(rows[i], Clean(cell))
				}
			}
			// Stack narrow tables without truncating identifiers or instructions.
			if width < 60 {
				for i, row := range rows {
					if i > 0 {
						parts = append(parts, "")
					}
					for col, value := range row {
						label := ""
						if col < len(section.Headers) {
							label = Clean(section.Headers[col]) + ": "
						}
						parts = append(parts, lipgloss.NewStyle().Width(width).Render(label+value))
					}
				}
			} else {
				headers := make([]string, len(section.Headers))
				for i, h := range section.Headers {
					headers[i] = Clean(h)
				}
				labelWidth := 0
				for _, row := range rows {
					if len(row) > 0 {
						labelWidth = max(labelWidth, lipgloss.Width(row[0]))
					}
				}
				if len(headers) > 0 {
					labelWidth = max(labelWidth, lipgloss.Width(headers[0]))
				}
				labelWidth = min(labelWidth+2, width/3)
				t := table.New().Rows(rows...).Width(width).Wrap(true).
					Border(lipgloss.HiddenBorder()).BorderTop(false).BorderBottom(false).
					BorderLeft(false).BorderRight(false).BorderColumn(false).BorderHeader(false).
					StyleFunc(func(row, col int) lipgloss.Style {
						s := lipgloss.NewStyle().PaddingRight(2)
						if row == table.HeaderRow || col == 0 {
							s = s.Inherit(r.Theme.Label)
						}
						if col == 0 {
							s = s.Width(labelWidth)
						}
						return s
					})
				if len(headers) > 0 {
					t.Headers(headers...)
				}
				parts = append(parts, t.Render())
			}
		}
		if len(parts) > 0 {
			blocks = append(blocks, strings.Join(parts, "\n"))
		}
	}
	result := strings.Join(blocks, "\n\n")
	// Plain output contains no cursor movement, color or hyperlinks, even when
	// CLICOLOR_FORCE is set. Avoid global Lip Gloss output/profile state.
	if !r.Styled {
		result = ansi.Strip(result)
	}
	lines := strings.Split(result, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n") + "\n"
}

func (r Renderer) Write(d Document) error {
	output := r.Output
	if r.Styled {
		output = colorprofile.NewWriter(output, os.Environ())
	}
	_, err := io.WriteString(output, r.Render(d))
	return err
}

func Row(label string, value any) []string { return []string{label, fmt.Sprint(value)} }

func Fields(title string, rows ...[]string) Section { return Section{Title: title, Rows: rows} }

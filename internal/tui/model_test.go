package tui

import (
	"strings"
	"testing"
	"time"

	"girc/internal/irc"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestRenderFrameDimensions(t *testing.T) {
	rendered := renderFrame("hello", 40, 5)

	if got := lipgloss.Width(rendered); got != 40 {
		t.Fatalf("frame width = %d, want 40", got)
	}
	if got := lipgloss.Height(rendered); got != 5 {
		t.Fatalf("frame height = %d, want 5", got)
	}
}

func TestRenderFrameCompactDimensions(t *testing.T) {
	rendered := renderFrame("hello", 40, 3)

	if got := lipgloss.Width(rendered); got != 40 {
		t.Fatalf("compact frame width = %d, want 40", got)
	}
	if got := lipgloss.Height(rendered); got != 3 {
		t.Fatalf("compact frame height = %d, want 3", got)
	}
}

func TestParseHostPort(t *testing.T) {
	tests := []struct {
		input       string
		defaultPort int
		wantHost    string
		wantPort    int
	}{
		{"irc.example.test", 6697, "irc.example.test", 6697},
		{"irc.example.test:6667", 6697, "irc.example.test", 6667},
		{"[::1]:6697", 6667, "::1", 6697},
		{"::1", 6697, "::1", 6697},
	}

	for _, test := range tests {
		host, port, err := parseHostPort(test.input, test.defaultPort)
		if err != nil {
			t.Fatalf("parseHostPort(%q) returned error: %v", test.input, err)
		}
		if host != test.wantHost || port != test.wantPort {
			t.Fatalf("parseHostPort(%q) = %q, %d; want %q, %d", test.input, host, port, test.wantHost, test.wantPort)
		}
	}
}

func TestRenderMessageIncludesTimestampAndSender(t *testing.T) {
	message := chatMessage{
		Kind: messageIncoming,
		At:   time.Date(2026, 6, 4, 13, 37, 0, 0, time.UTC),
		From: "alice",
		Text: "hello",
	}

	rendered := renderMessage(message, 80)
	for _, want := range []string{"13:37", "<alice>", "hello"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered message does not contain %q: %q", want, rendered)
		}
	}
}

func TestSanitizeDisplayTextStripsTerminalEscapes(t *testing.T) {
	input := "hello \x1b[31mred\x1b[0m \x1b]52;c;c2VjcmV0\x07clipboard"
	got := sanitizeDisplayText(input)

	if strings.Contains(got, "\x1b") || strings.Contains(got, "[31m") || strings.Contains(got, "]52") {
		t.Fatalf("sanitizeDisplayText left escape data behind: %q", got)
	}
	if got != "hello red clipboard" {
		t.Fatalf("sanitizeDisplayText = %q", got)
	}
}

func TestAddMessageSanitizesIncomingText(t *testing.T) {
	model := New(irc.Config{})
	model.addIncoming("#zig\x1b[31m", "alice\x1b[31m", "hello\x1b]52;c;c2VjcmV0\x07")

	messages := model.buffers["#zig"]
	if len(messages) != 1 {
		t.Fatalf("messages = %d", len(messages))
	}
	if messages[0].From != "alice" || messages[0].Text != "hello" {
		t.Fatalf("message was not sanitized: %#v", messages[0])
	}
}

func TestBufferPreview(t *testing.T) {
	model := New(irc.Config{})
	model.addIncoming("#zig", "alice", "hello")

	if got := model.bufferPreview("#zig"); got != "alice: hello" {
		t.Fatalf("preview = %q", got)
	}
}

func TestRenderNavItemKeepsUserCountNextToChannel(t *testing.T) {
	model := New(irc.Config{})
	model.active = "#go-nuts"
	model.users["#go-nuts"] = 709

	rendered := model.renderNavItem("#go-nuts", 24)
	if !strings.Contains(rendered, "#go-nuts 709") {
		t.Fatalf("nav item does not keep count next to channel: %q", rendered)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := map[time.Duration]string{
		23 * time.Millisecond:   "23ms",
		1400 * time.Millisecond: "1.4s",
		2 * time.Minute:         "2m",
		3 * time.Hour:           "3h",
	}

	for input, want := range tests {
		if got := formatDuration(input); got != want {
			t.Fatalf("formatDuration(%s) = %q, want %q", input, got, want)
		}
	}
}

func TestViewDoesNotExceedTerminalWidth(t *testing.T) {
	for _, size := range []tea.WindowSizeMsg{
		teaWindowSize(40, 15),
		teaWindowSize(80, 24),
		teaWindowSize(120, 32),
	} {
		model := New(irc.Config{Server: "irc.libera.chat", Nick: "tester", Channel: "#zig", TLS: true})
		updated, _ := model.Update(size)
		model = updated.(Model)

		for index, line := range strings.Split(model.View(), "\n") {
			if got := lipgloss.Width(line); got > size.Width {
				t.Fatalf("%dx%d line %d width = %d, want <= %d: %q", size.Width, size.Height, index+1, got, size.Width, line)
			}
		}
	}
}

func TestViewDoesNotExceedTerminalHeight(t *testing.T) {
	for _, size := range []tea.WindowSizeMsg{
		teaWindowSize(40, 15),
		teaWindowSize(80, 24),
		teaWindowSize(120, 32),
	} {
		model := New(irc.Config{Server: "irc.libera.chat", Nick: "tester", Channel: "#zig", TLS: true})
		updated, _ := model.Update(size)
		model = updated.(Model)

		if got := lipgloss.Height(model.View()); got > size.Height {
			t.Fatalf("%dx%d view height = %d, want <= %d", size.Width, size.Height, got, size.Height)
		}
	}
}

func teaWindowSize(width, height int) tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: width, Height: height}
}

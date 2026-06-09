package tui

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"girc/internal/irc"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const statusBuffer = "status"

const appBackground = "#071015"
const appBackgroundANSI = "\x1b[48;2;7;16;21m"
const resetANSI = "\x1b[0m"

type Model struct {
	cfg    irc.Config
	client *irc.Client

	input    textinput.Model
	viewport viewport.Model

	buffers      map[string][]chatMessage
	bufferOrder  []string
	unread       map[string]int
	topics       map[string]string
	users        map[string]int
	active       string
	lastActivity time.Time
	lastPing     time.Duration
	pingToken    string
	pingSentAt   time.Time

	width      int
	height     int
	connecting bool
	connected  bool
	status     string
}

type connectedMsg struct {
	client *irc.Client
}

type ircEventMsg irc.Event

type connectErrMsg struct {
	err error
}

type closedMsg struct{}

type timeTickMsg time.Time

type messageKind int

const (
	messageSystem messageKind = iota
	messageIncoming
	messageOutgoing
	messageAction
	messageError
	messageNotice
)

type chatMessage struct {
	Kind     messageKind
	At       time.Time
	From     string
	Text     string
	Outgoing bool
}

var (
	appStyle   = lipgloss.NewStyle().Background(lipgloss.Color(appBackground))
	frameStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#d6d0c8")).
			Background(lipgloss.Color(appBackground)).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#2f8f3b"))
	sectionTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#20f6ff")).
				Background(lipgloss.Color(appBackground)).
				Bold(true)
	activeItemStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#f4fff3")).
			Background(lipgloss.Color("#136f35")).
			Padding(0, 1)
	itemStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#d6d0c8")).
			Background(lipgloss.Color(appBackground)).
			Padding(0, 1)
	accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#20f6ff")).Background(lipgloss.Color(appBackground))
	yellowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffd84d")).Background(lipgloss.Color(appBackground)).Bold(true)
	greenStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#7dff63")).Background(lipgloss.Color(appBackground))
	blueStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#64c8ff")).Background(lipgloss.Color(appBackground))
	pinkStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff66c4")).Background(lipgloss.Color(appBackground))
	orangeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffb84d")).Background(lipgloss.Color(appBackground))
	metaStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#8f9399")).Background(lipgloss.Color(appBackground))
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff5f87")).Background(lipgloss.Color(appBackground))
	noticeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#bd8cff")).Background(lipgloss.Color(appBackground))
	meStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffb84d")).Background(lipgloss.Color(appBackground))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#b7ada4")).Background(lipgloss.Color(appBackground))
)

func New(cfg irc.Config) Model {
	input := textinput.New()
	placeholderTarget := firstConfiguredChannel(cfg)
	if placeholderTarget == "" {
		placeholderTarget = "current chat"
	}
	input.Placeholder = "message " + placeholderTarget
	input.Prompt = "› "
	input.Focus()
	input.CharLimit = 4096
	input.PromptStyle = accentStyle
	input.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#d6d0c8")).Background(lipgloss.Color(appBackground))
	input.PlaceholderStyle = metaStyle
	input.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#7dff63"))

	vp := viewport.New(0, 0)

	model := Model{
		cfg:          cfg,
		input:        input,
		viewport:     vp,
		buffers:      map[string][]chatMessage{statusBuffer: nil},
		bufferOrder:  []string{statusBuffer},
		unread:       make(map[string]int),
		topics:       make(map[string]string),
		users:        make(map[string]int),
		active:       statusBuffer,
		lastActivity: time.Now(),
		status:       "starting",
	}
	model.addSystem(statusBuffer, "girc TUI")
	model.addSystem(statusBuffer, "Type /help for commands.")
	return model
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.connectCmd(), tickCmd())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		metrics := m.layoutMetrics()
		m.input.Width = max(10, metrics.inputWidth-6)
		m.viewport.Width = max(10, metrics.centerWidth-4)
		m.viewport.Height = max(1, metrics.bodyHeight-2)
		m.refreshViewport()
	case timeTickMsg:
		m.sendPingProbe()
		cmds = append(cmds, tickCmd())
	case connectedMsg:
		m.client = msg.client
		m.connected = true
		m.connecting = false
		m.status = "connected to " + m.cfg.Server
		m.lastActivity = time.Now()
		m.lastPing = 0
		m.addSystem(statusBuffer, "connected to "+m.serverAddress())
		cmds = append(cmds, waitEventCmd(m.client))
	case connectErrMsg:
		m.connecting = false
		m.connected = false
		m.status = "connection failed"
		m.addError(statusBuffer, "connect failed: "+msg.err.Error())
	case ircEventMsg:
		event := irc.Event(msg)
		if event.Kind == irc.EventPong {
			m.handlePong(event)
		}
		m.lastActivity = time.Now()
		m.applyEvent(event)
		if m.client != nil && event.Kind != irc.EventClosed {
			cmds = append(cmds, waitEventCmd(m.client))
		}
	case closedMsg:
		m.connected = false
		m.connecting = false
		m.status = "disconnected"
		m.addError(statusBuffer, "connection closed")
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			if m.client != nil {
				m.client.Quit("leaving")
			}
			return m, tea.Quit
		case "tab":
			m.nextBuffer()
		case "shift+tab":
			m.prevBuffer()
		case "enter":
			cmd := m.submit()
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}

	var inputCmd tea.Cmd
	m.input, inputCmd = m.input.Update(msg)
	cmds = append(cmds, inputCmd)

	var viewportCmd tea.Cmd
	m.viewport, viewportCmd = m.viewport.Update(msg)
	cmds = append(cmds, viewportCmd)

	return m, tea.Batch(cmds...)
}

func (m Model) View() string {
	if m.width == 0 {
		return "starting..."
	}

	metrics := m.layoutMetrics()
	m.viewport.Width = max(10, metrics.centerWidth-4)
	m.viewport.Height = max(1, metrics.bodyHeight-2)

	sections := make([]string, 0, 5)
	if metrics.topMargin > 0 {
		sections = append(sections, "")
	}
	sections = append(sections, m.renderTopbar(metrics.width))
	if metrics.heroHeight > 0 {
		sections = append(sections, m.renderHero(metrics.width, metrics.heroHeight))
	}
	sections = append(sections, m.renderBody(metrics), m.renderInput(metrics.inputWidth), m.renderStatusbar(metrics.inputWidth))
	view := lipgloss.JoinVertical(lipgloss.Left, sections...)
	return renderScreen(view, m.width, m.height)
}

func tickCmd() tea.Cmd {
	return tea.Tick(30*time.Second, func(t time.Time) tea.Msg {
		return timeTickMsg(t)
	})
}

func (m Model) connectCmd() tea.Cmd {
	if m.cfg.Server == "" {
		return nil
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		client := irc.NewClient(m.cfg)
		if err := client.Connect(ctx); err != nil {
			return connectErrMsg{err: err}
		}
		return connectedMsg{client: client}
	}
}

func waitEventCmd(client *irc.Client) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-client.Events()
		if !ok {
			return closedMsg{}
		}
		return ircEventMsg(event)
	}
}

func (m *Model) sendPingProbe() {
	if !m.connected || m.client == nil {
		return
	}
	if !m.pingSentAt.IsZero() && time.Since(m.pingSentAt) < 10*time.Second {
		return
	}
	m.pingToken = "girc-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	m.pingSentAt = time.Now()
	m.client.Ping(m.pingToken)
}

func (m *Model) handlePong(event irc.Event) {
	if event.Text == "" || event.Text != m.pingToken || m.pingSentAt.IsZero() {
		return
	}
	m.lastPing = time.Since(m.pingSentAt).Truncate(time.Millisecond)
	m.pingSentAt = time.Time{}
	m.pingToken = ""
}

func (m *Model) submit() tea.Cmd {
	value := strings.TrimSpace(m.input.Value())
	m.input.SetValue("")
	if value == "" {
		return nil
	}

	if strings.HasPrefix(value, "/") {
		return m.handleCommand(value)
	}

	if !m.connected || m.client == nil {
		m.addError(statusBuffer, "not connected")
		return nil
	}
	if m.active == statusBuffer {
		m.addError(statusBuffer, "join a channel or switch to a query first")
		return nil
	}

	target := m.active
	m.client.Privmsg(target, value)
	m.addOutgoing(target, m.client.Nick(), value)
	return nil
}

func (m *Model) handleCommand(value string) tea.Cmd {
	name, rest := splitCommand(value)

	switch name {
	case "help":
		m.showHelp()
	case "connect":
		if m.connected || m.connecting {
			m.addError(statusBuffer, "already connected")
			return nil
		}
		cfg, err := parseConnect(rest, m.cfg)
		if err != nil {
			m.addError(statusBuffer, err.Error())
			return nil
		}
		m.cfg = cfg
		m.connecting = true
		m.status = "connecting to " + m.serverAddress()
		m.addSystem(statusBuffer, "connecting to "+m.serverAddress())
		return m.connectCmd()
	case "join", "j":
		if !m.requireConnection() {
			return nil
		}
		channel := strings.TrimSpace(rest)
		if channel == "" {
			m.addError(statusBuffer, "usage: /join #channel")
			return nil
		}
		if !strings.HasPrefix(channel, "#") {
			channel = "#" + channel
		}
		m.ensureBuffer(channel)
		m.active = channel
		m.refreshViewport()
		m.client.Join(channel)
	case "part":
		if !m.requireConnection() {
			return nil
		}
		channel := strings.TrimSpace(rest)
		if channel == "" {
			channel = m.active
		}
		if channel == statusBuffer {
			m.addError(statusBuffer, "usage: /part #channel")
			return nil
		}
		m.client.Part(channel)
	case "nick":
		if !m.requireConnection() {
			return nil
		}
		nick := strings.TrimSpace(rest)
		if nick == "" {
			m.addError(statusBuffer, "usage: /nick newnick")
			return nil
		}
		m.client.NickChange(nick)
	case "ns", "nickserv":
		if !m.requireConnection() {
			return nil
		}
		text := strings.TrimSpace(rest)
		if text == "" {
			m.addError(statusBuffer, "usage: /ns NickServ-command")
			return nil
		}
		m.client.Privmsg("NickServ", text)
		m.addSystem(statusBuffer, "sent command to NickServ")
	case "msg", "query", "q":
		if !m.requireConnection() {
			return nil
		}
		target, text := splitFirst(rest)
		if target == "" {
			m.addError(statusBuffer, "usage: /msg nick message")
			return nil
		}
		m.ensureBuffer(target)
		m.active = target
		if text != "" {
			m.client.Privmsg(target, text)
			if isServiceTarget(target) {
				m.addSystem(statusBuffer, "sent command to "+target)
			} else {
				m.addOutgoing(target, m.client.Nick(), text)
			}
		}
		m.refreshViewport()
	case "me":
		if !m.requireConnection() {
			return nil
		}
		if m.active == statusBuffer {
			m.addError(statusBuffer, "switch to a channel or query first")
			return nil
		}
		text := strings.TrimSpace(rest)
		if text == "" {
			m.addError(statusBuffer, "usage: /me action")
			return nil
		}
		m.client.Action(m.active, text)
		m.addAction(m.active, m.client.Nick(), text, true)
	case "switch", "buffer", "b":
		target := strings.TrimSpace(rest)
		if target == "" {
			m.addError(statusBuffer, "usage: /switch buffer-or-number")
			return nil
		}
		m.switchBuffer(target)
	case "raw":
		if !m.requireConnection() {
			return nil
		}
		line := strings.TrimSpace(rest)
		if line == "" {
			m.addError(statusBuffer, "usage: /raw IRC COMMAND")
			return nil
		}
		m.client.SendRaw(line)
	case "whois":
		if !m.requireConnection() {
			return nil
		}
		nick := strings.TrimSpace(rest)
		if nick == "" {
			m.addError(statusBuffer, "usage: /whois nick")
			return nil
		}
		m.client.Send("WHOIS", nick)
	case "quit":
		message := strings.TrimSpace(rest)
		if m.client != nil {
			m.client.Quit(message)
		}
		return tea.Quit
	default:
		m.addError(statusBuffer, "unknown command: /"+name)
	}

	return nil
}

func (m *Model) applyEvent(event irc.Event) {
	buffer := event.Buffer
	if buffer == "" {
		buffer = statusBuffer
	}

	switch event.Kind {
	case irc.EventMessage:
		m.addIncoming(buffer, event.From, event.Text)
	case irc.EventAction:
		m.addAction(buffer, event.From, event.Text, false)
	case irc.EventNotice:
		prefix := "-notice-"
		if event.From != "" {
			prefix = "-" + event.From + "-"
		}
		m.addNotice(buffer, prefix+" "+event.Text)
	case irc.EventError:
		m.addError(buffer, event.Text)
	case irc.EventClosed:
		m.connected = false
		m.status = "disconnected"
		m.addError(statusBuffer, event.Text)
	case irc.EventPong:
		return
	default:
		m.updateChannelMeta(buffer, event.Text)
		m.addSystem(buffer, event.Text)
		if buffer == statusBuffer && event.Text == "connected" {
			m.sendPingProbe()
		}
		if buffer != statusBuffer && strings.HasPrefix(event.Text, "joined ") {
			m.active = buffer
			m.unread[buffer] = 0
			m.refreshViewport()
		}
	}
}

func (m *Model) updateChannelMeta(buffer, text string) {
	buffer = sanitizeDisplayText(buffer)
	text = sanitizeDisplayText(text)
	if !strings.HasPrefix(buffer, "#") {
		return
	}
	if topic, ok := strings.CutPrefix(text, "topic: "); ok {
		m.topics[buffer] = topic
		return
	}
	if strings.HasPrefix(text, "names: ") {
		fields := strings.Fields(text)
		if len(fields) >= 2 {
			if count, err := strconv.Atoi(fields[1]); err == nil {
				m.users[buffer] = count
			}
		}
	}
}

func (m *Model) showHelp() {
	lines := []string{
		"commands:",
		"  /connect host[:port] [nick]",
		"  /join #channel",
		"  /part [#channel]",
		"  /msg nick message",
		"  /ns NickServ-command",
		"  /me action",
		"  /nick newnick",
		"  /whois nick",
		"  /switch buffer-or-number",
		"  /raw COMMAND",
		"  /quit [message]",
		"keys: Tab / Shift+Tab switch buffers, Ctrl+C quits",
	}
	for _, line := range lines {
		m.addSystem(statusBuffer, line)
	}
}

func isServiceTarget(target string) bool {
	switch strings.ToLower(target) {
	case "nickserv", "chanserv", "alis", "memoserv":
		return true
	default:
		return false
	}
}

func (m *Model) requireConnection() bool {
	if m.connected && m.client != nil {
		return true
	}
	m.addError(statusBuffer, "not connected")
	return false
}

func (m *Model) addSystem(buffer, text string) {
	m.addMessage(buffer, chatMessage{Kind: messageSystem, Text: text})
}

func (m *Model) addError(buffer, text string) {
	m.addMessage(buffer, chatMessage{Kind: messageError, Text: text})
}

func (m *Model) addNotice(buffer, text string) {
	m.addMessage(buffer, chatMessage{Kind: messageNotice, Text: text})
}

func (m *Model) addIncoming(buffer, from, text string) {
	m.addMessage(buffer, chatMessage{Kind: messageIncoming, From: from, Text: text})
}

func (m *Model) addOutgoing(buffer, from, text string) {
	m.addMessage(buffer, chatMessage{Kind: messageOutgoing, From: from, Text: text, Outgoing: true})
}

func (m *Model) addAction(buffer, from, text string, outgoing bool) {
	m.addMessage(buffer, chatMessage{Kind: messageAction, From: from, Text: text, Outgoing: outgoing})
}

func (m *Model) addMessage(buffer string, message chatMessage) {
	buffer = sanitizeDisplayText(buffer)
	message.From = sanitizeDisplayText(message.From)
	message.Text = sanitizeDisplayText(message.Text)
	m.ensureBuffer(buffer)
	if message.At.IsZero() {
		message.At = time.Now()
	}
	m.buffers[buffer] = append(m.buffers[buffer], message)
	if len(m.buffers[buffer]) > 2000 {
		m.buffers[buffer] = m.buffers[buffer][len(m.buffers[buffer])-2000:]
	}
	if buffer == m.active {
		m.refreshViewport()
		return
	}
	m.unread[buffer]++
}

func (m *Model) ensureBuffer(buffer string) {
	buffer = sanitizeDisplayText(buffer)
	if buffer == "" {
		buffer = statusBuffer
	}
	if _, ok := m.buffers[buffer]; ok {
		return
	}
	m.buffers[buffer] = nil
	m.bufferOrder = append(m.bufferOrder, buffer)
}

func (m *Model) refreshViewport() {
	content := m.renderMessages(m.active, m.viewport.Width)
	m.viewport.SetContent(content)
	m.viewport.GotoBottom()
}

func (m Model) renderMessages(buffer string, width int) string {
	messages := m.buffers[buffer]
	if len(messages) == 0 {
		return metaStyle.Width(width).Align(lipgloss.Center).Render("No messages yet")
	}

	lines := make([]string, 0, len(messages))
	for _, message := range messages {
		lines = append(lines, renderMessage(message, width))
	}
	return strings.Join(lines, "\n")
}

func renderMessage(message chatMessage, width int) string {
	width = max(20, width)
	switch message.Kind {
	case messageIncoming, messageOutgoing:
		return renderChatLine(message, width)
	case messageAction:
		text := fmt.Sprintf("%s %s", message.From, message.Text)
		if message.Outgoing {
			text = "you " + message.Text
		}
		return renderSystemLine(message.At, meStyle.Render("* "+text), width)
	case messageError:
		return renderSystemLine(message.At, errorStyle.Render(message.Text), width)
	case messageNotice:
		return renderSystemLine(message.At, noticeStyle.Render(message.Text), width)
	default:
		return renderSystemLine(message.At, metaStyle.Render(message.Text), width)
	}
}

func renderChatLine(message chatMessage, width int) string {
	name := message.From
	nameStyle := nickColor(name)
	if message.Outgoing {
		name = "you"
		nameStyle = orangeStyle
	}

	prefix := fmt.Sprintf("%s  %s ", metaStyle.Render(message.At.Format("15:04")), nameStyle.Render("<"+name+">"))
	available := max(10, width-lipgloss.Width(prefix))
	wrapped := lipgloss.NewStyle().Foreground(lipgloss.Color("#d6d0c8")).Background(lipgloss.Color(appBackground)).Width(available).Render(message.Text)
	lines := strings.Split(wrapped, "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = strings.Repeat(" ", lipgloss.Width(prefix)) + lines[i]
	}
	return prefix + strings.Join(lines, "\n")
}

func renderSystemLine(at time.Time, text string, width int) string {
	prefix := metaStyle.Render(at.Format("15:04")) + "  "
	available := max(10, width-lipgloss.Width(prefix))
	wrapped := lipgloss.NewStyle().Background(lipgloss.Color(appBackground)).Width(available).Render(text)
	lines := strings.Split(wrapped, "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = strings.Repeat(" ", lipgloss.Width(prefix)) + lines[i]
	}
	return prefix + strings.Join(lines, "\n")
}

func (m Model) renderBody(metrics layoutMetrics) string {
	panels := []string{}
	if metrics.leftWidth > 0 {
		panels = append(panels, m.renderSidebar(metrics.leftWidth, metrics.bodyHeight))
	}
	panels = append(panels, m.renderChatPanel(metrics.centerWidth, metrics.bodyHeight))
	if metrics.rightWidth > 0 {
		panels = append(panels, m.renderProfile(metrics.rightWidth, metrics.bodyHeight))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, panels...)
}

func (m Model) renderSidebar(width, height int) string {
	return m.renderChannelList(width, height)
}

func (m Model) renderChannelList(width, height int) string {
	contentWidth := max(8, width-4)
	lines := []string{sectionTitleStyle.Render(fmt.Sprintf("Channels (%d)", len(m.channelBuffers())))}
	for _, buffer := range m.channelBuffers() {
		lines = append(lines, m.renderNavItem(buffer, contentWidth))
	}
	lines = append(lines, "", metaStyle.Render(strings.Repeat("-", max(4, contentWidth))), "", pinkStyle.Render(fmt.Sprintf("Direct Messages (%d)", len(m.directBuffers()))))
	for _, buffer := range m.directBuffers() {
		lines = append(lines, m.renderDMItem(buffer, contentWidth))
	}
	if len(m.directBuffers()) == 0 {
		lines = append(lines, metaStyle.Render("none"))
	}
	return renderFrame(strings.Join(lines, "\n"), width, height)
}

func (m Model) renderNavItem(buffer string, width int) string {
	label := buffer
	if strings.HasPrefix(buffer, "#") {
		if users := m.users[buffer]; users > 0 {
			label = fmt.Sprintf("%s %d", label, users)
		}
	}
	if unread := m.unread[buffer]; unread > 0 {
		label = fmt.Sprintf("%s (%d)", label, unread)
	}
	label = truncate(label, max(1, width-2))
	if buffer == m.active {
		return activeItemStyle.Width(width).Render("› " + label)
	}
	return itemStyle.Width(width).Render("  " + label)
}

// renderDMItem is a nav item with a presence dot pinned to the right edge. A
// buffer with unread traffic is treated as "active" and shown green.
func (m Model) renderDMItem(buffer string, width int) string {
	label := buffer
	if unread := m.unread[buffer]; unread > 0 {
		label = fmt.Sprintf("%s (%d)", label, unread)
	}
	dot := metaStyle.Render("○")
	if m.unread[buffer] > 0 {
		dot = greenStyle.Render("●")
	}
	style := itemStyle
	prefix := "  "
	if buffer == m.active {
		style = activeItemStyle
		prefix = "› "
	}
	text := prefix + truncate(label, max(1, width-5))
	pad := max(1, (width-2)-lipgloss.Width(text)-1)
	return style.Width(width).Render(text + strings.Repeat(" ", pad) + dot)
}

func (m Model) renderChatPanel(width, height int) string {
	contentWidth := max(10, width-4)
	header := m.renderChatHeader(contentWidth)
	bodyHeight := max(1, height-4)
	m.viewport.Height = bodyHeight
	content := lipgloss.JoinVertical(lipgloss.Left, header, m.viewport.View())
	return renderFrame(content, width, height)
}

func (m Model) bufferPreview(buffer string) string {
	messages := m.buffers[buffer]
	if len(messages) == 0 {
		return ""
	}

	message := messages[len(messages)-1]
	switch message.Kind {
	case messageIncoming:
		return message.From + ": " + message.Text
	case messageOutgoing:
		return "you: " + message.Text
	case messageAction:
		return "* " + message.From + " " + message.Text
	default:
		return message.Text
	}
}

func (m Model) renderTopbar(width int) string {
	title := m.active
	if m.active == statusBuffer {
		title = "Status"
	}

	left := greenStyle.Render(m.localTimeLabel())
	center := accentStyle.Render(title)
	right := fmt.Sprintf("server: %s  |  nick: %s  |  %s", yellowStyle.Render(m.serverAddress()), m.nickLabel(), m.statusLabel())
	contentWidth := max(10, width-4)
	line := spreadLine(left, center, right, contentWidth)
	return renderFrame(line, width, 3)
}

func (m Model) renderChatHeader(width int) string {
	topic := m.topics[m.active]
	if topic == "" {
		topic = "Welcome to " + m.active + " - Be excellent to each other!"
	}
	users := ""
	if count := m.users[m.active]; count > 0 {
		users = greenStyle.Render(fmt.Sprintf("%d users", count))
	}
	left := accentStyle.Render(m.active) + "  " + dimStyle.Render(truncate(topic, max(8, width/2)))
	return spreadLine(left, "", users, width)
}

func (m Model) renderInput(width int) string {
	return renderFrame(m.input.View(), width, 3)
}

func (m Model) renderStatusbar(width int) string {
	contentWidth := max(10, width-4)
	items := []string{
		"nick: " + yellowStyle.Render(m.currentNick()),
		"channel: " + yellowStyle.Render(m.active),
		"server: " + yellowStyle.Render(truncate(m.cfg.Server, 28)),
		"TLS: " + m.tlsLabel(),
		"ping: " + m.pingLabel(),
	}
	separator := metaStyle.Render(" | ")
	line := " " + items[0]
	for _, item := range items[1:] {
		next := line + separator + item
		if lipgloss.Width(next) > contentWidth {
			break
		}
		line = next
	}
	return renderFrame(line, width, 3)
}

func (m Model) renderHero(width, height int) string {
	logo := renderLogo()
	info := strings.Join([]string{
		accentStyle.Bold(true).Render("Go IRC Client"),
		"",
		dimStyle.Render("lightweight  •  fast  •  extensible"),
		dimStyle.Render("crafted in Go, powered by nostalgia"),
		"",
		accentStyle.Render("github.com/honeynil/girc"),
	}, "\n")

	contentWidth := max(20, width-4)
	logoWidth := clamp(contentWidth*3/5, 38, 72)
	infoWidth := max(1, contentWidth-logoWidth)
	content := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Background(lipgloss.Color(appBackground)).Width(logoWidth).PaddingLeft(2).Render(logo),
		lipgloss.NewStyle().Background(lipgloss.Color(appBackground)).Width(infoWidth).PaddingTop(1).PaddingLeft(2).Render(info),
	)
	return renderFrame(content, width, height)
}

func (m Model) renderProfile(width, height int) string {
	contentWidth := max(10, width-4)
	lines := append([]string{
		sectionTitleStyle.Render("Profile"),
		"",
	}, m.profileIdentityRows(contentWidth)...)
	lines = append(lines,
		m.profileRow("Status:", m.statusLabel()),
		m.profileRow("Joined:", blueStyle.Render(fmt.Sprintf("%d channels", len(m.channelBuffers())))),
		"",
		metaStyle.Render(strings.Repeat("-", max(4, contentWidth))),
		"",
		sectionTitleStyle.Render("Buffer"),
		"",
		m.profileRow("Active:", accentStyle.Render(truncate(m.active, max(1, contentWidth-8)))),
		m.profileRow("Users:", m.activeUsersLabel()),
		m.profileRow("Unread:", pinkStyle.Render(strconv.Itoa(m.totalUnread()))),
		m.profileRow("Last:", m.lastEventLabel()),
		"",
		metaStyle.Render(strings.Repeat("-", max(4, contentWidth))),
		"",
		sectionTitleStyle.Render("Signal"),
		"",
		m.profileRow("Port:", strconv.Itoa(m.effectivePort())),
		m.profileRow("Level:", m.signalLabel()),
		"",
		metaStyle.Render(strings.Repeat("-", max(4, contentWidth))),
		"",
		sectionTitleStyle.Render("Quick Actions"),
		"",
		quickAction("⌁", "/join", "Join a channel", contentWidth),
		quickAction(">", "/msg", "Send a message", contentWidth),
		quickAction("◔", "/nick", "Change nickname", contentWidth),
		quickAction("?", "/whois", "Query a user", contentWidth),
		quickAction("⏻", "/quit", "Disconnect", contentWidth),
	)
	return renderFrame(strings.Join(lines, "\n"), width, height)
}

func renderLogo() string {
	rows := []string{
		"  ██████╗ ██╗██████╗  ██████╗",
		" ██╔════╝ ██║██╔══██╗██╔════╝",
		" ██║  ███╗██║██████╔╝██║     ",
		" ██║   ██║██║██╔══██╗██║     ",
		" ╚██████╔╝██║██║  ██║╚██████╗",
		"  ╚═════╝ ╚═╝╚═╝  ╚═╝ ╚═════╝",
	}
	colors := []lipgloss.Color{
		lipgloss.Color("#fff34d"),
		lipgloss.Color("#d8ff4d"),
		lipgloss.Color("#86ff5f"),
		lipgloss.Color("#32e6a6"),
		lipgloss.Color("#20f6ff"),
	}
	rendered := make([]string, len(rows))
	for y, row := range rows {
		var builder strings.Builder
		for x, r := range row {
			if r == ' ' {
				builder.WriteRune(r)
				continue
			}
			color := colors[min(len(colors)-1, x*len(colors)/max(1, len(row)))]
			builder.WriteString(lipgloss.NewStyle().Foreground(color).Bold(true).Render(string(r)))
		}
		rendered[y] = builder.String()
	}
	return strings.Join(rendered, "\n")
}

func (m Model) profileRow(label, value string) string {
	return fmt.Sprintf("%-8s %s", metaStyle.Render(label), value)
}

func (m Model) profileIdentityRows(width int) []string {
	nick := m.currentNick()
	rows := []string{m.profileRow("Nick:", yellowStyle.Render(truncate(nick, max(1, width-8))))}
	seen := map[string]bool{strings.ToLower(nick): true}

	if name := strings.TrimSpace(m.cfg.RealName); name != "" && !seen[strings.ToLower(name)] {
		rows = append(rows, m.profileRow("Name:", dimStyle.Render(truncate(name, max(1, width-8)))))
		seen[strings.ToLower(name)] = true
	}
	if user := strings.TrimSpace(m.cfg.User); user != "" && !seen[strings.ToLower(user)] {
		rows = append(rows, m.profileRow("User:", dimStyle.Render(truncate(user, max(1, width-8)))))
	}
	return rows
}

func quickAction(icon, command, description string, width int) string {
	commandPart := accentStyle.Render(icon) + " " + yellowStyle.Render(command)
	spaces := max(1, 10-lipgloss.Width(icon+" "+command))
	descriptionWidth := width - lipgloss.Width(commandPart) - spaces
	if descriptionWidth <= 0 {
		return commandPart
	}
	return commandPart + strings.Repeat(" ", spaces) + dimStyle.Render(truncate(description, descriptionWidth))
}

func (m Model) channelBuffers() []string {
	buffers := make([]string, 0, len(m.bufferOrder))
	for _, buffer := range m.bufferOrder {
		if strings.HasPrefix(buffer, "#") {
			buffers = append(buffers, buffer)
		}
	}
	if len(buffers) == 0 {
		buffers = append(buffers, statusBuffer)
	}
	return buffers
}

func (m Model) directBuffers() []string {
	buffers := make([]string, 0, len(m.bufferOrder))
	for _, buffer := range m.bufferOrder {
		if buffer == statusBuffer || strings.HasPrefix(buffer, "#") {
			continue
		}
		buffers = append(buffers, buffer)
	}
	return buffers
}

func (m Model) currentNick() string {
	if m.connected && m.client != nil {
		return m.client.Nick()
	}
	if m.cfg.Nick != "" {
		return m.cfg.Nick
	}
	return "guest"
}

func firstConfiguredChannel(cfg irc.Config) string {
	channels := cfg.JoinChannels()
	if len(channels) == 0 {
		return ""
	}
	return channels[0]
}

func (m Model) nickLabel() string {
	return yellowStyle.Render(m.currentNick())
}

func (m Model) statusLabel() string {
	if m.connecting {
		return yellowStyle.Render("◐ connecting")
	}
	if m.connected {
		return greenStyle.Render("● online")
	}
	return errorStyle.Render("○ offline")
}

func (m Model) signalLabel() string {
	switch {
	case m.connected:
		return greenStyle.Render("▂▄▆█")
	case m.connecting:
		return yellowStyle.Render("▂▄▆") + metaStyle.Render("█")
	default:
		return metaStyle.Render("▂▄▆█")
	}
}

func (m Model) activeUsersLabel() string {
	if strings.HasPrefix(m.active, "#") {
		if users := m.users[m.active]; users > 0 {
			return greenStyle.Render(strconv.Itoa(users))
		}
		return metaStyle.Render("--")
	}
	if m.active == statusBuffer {
		return metaStyle.Render("status")
	}
	return metaStyle.Render("direct")
}

func (m Model) localTimeLabel() string {
	now := time.Now()
	_, offset := now.Zone()
	hours := offset / 3600
	label := fmt.Sprintf("%s UTC%+d", now.Format("15:04"), hours)
	return label
}

func (m Model) pingLabel() string {
	if !m.connected || m.lastPing <= 0 {
		return metaStyle.Render("--")
	}
	return greenStyle.Render(formatDuration(m.lastPing))
}

func (m Model) lastEventLabel() string {
	if m.lastActivity.IsZero() {
		return metaStyle.Render("--")
	}
	return metaStyle.Render(formatDuration(time.Since(m.lastActivity)))
}

func (m Model) totalUnread() int {
	total := 0
	for _, count := range m.unread {
		total += count
	}
	return total
}

func renderFrame(content string, width, height int) string {
	innerHeight := max(1, height-2)
	return frameStyle.
		Width(max(1, width-2)).
		Height(innerHeight).
		Render(fitLines(content, innerHeight))
}

func renderScreen(content string, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	lines := strings.Split(fitLines(content, height), "\n")
	for len(lines) < height {
		lines = append(lines, "")
	}
	for i, line := range lines {
		lines[i] = enforceBackground(appStyle.Width(width).Render(line))
	}
	return strings.Join(lines, "\n") + resetANSI
}

func enforceBackground(line string) string {
	line = strings.ReplaceAll(line, "\x1b[49m", appBackgroundANSI)
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0;48;2;7;16;21m")
	return appBackgroundANSI + line + resetANSI
}

func sanitizeDisplayText(value string) string {
	var builder strings.Builder
	for i := 0; i < len(value); {
		b := value[i]
		switch {
		case b == 0x1b:
			i = skipEscapeSequence(value, i)
		case b == '\t':
			builder.WriteByte(' ')
			i++
		case b < 0x20 || b == 0x7f:
			i++
		default:
			r, size := rune(value[i]), 1
			if b >= 0x80 {
				r, size = utf8.DecodeRuneInString(value[i:])
				if r == utf8.RuneError && size == 1 {
					i++
					continue
				}
				if r >= 0x80 && r <= 0x9f {
					i += size
					continue
				}
			}
			builder.WriteRune(r)
			i += size
		}
	}
	return builder.String()
}

func skipEscapeSequence(value string, start int) int {
	i := start + 1
	if i >= len(value) {
		return i
	}
	switch value[i] {
	case '[':
		i++
		for i < len(value) {
			if value[i] >= 0x40 && value[i] <= 0x7e {
				return i + 1
			}
			i++
		}
	case ']':
		i++
		for i < len(value) {
			if value[i] == 0x07 {
				return i + 1
			}
			if value[i] == 0x1b && i+1 < len(value) && value[i+1] == '\\' {
				return i + 2
			}
			i++
		}
	default:
		return i + 1
	}
	return i
}

func formatDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	switch {
	case duration < time.Second:
		return fmt.Sprintf("%dms", duration.Milliseconds())
	case duration < time.Minute:
		return fmt.Sprintf("%.1fs", float64(duration)/float64(time.Second))
	case duration < time.Hour:
		return fmt.Sprintf("%dm", int(duration/time.Minute))
	default:
		return fmt.Sprintf("%dh", int(duration/time.Hour))
	}
}

func fitLines(content string, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= height {
		return content
	}
	return strings.Join(lines[:height], "\n")
}

func spreadLine(left, center, right string, width int) string {
	leftWidth := lipgloss.Width(left)
	centerWidth := lipgloss.Width(center)
	rightWidth := lipgloss.Width(right)
	if leftWidth+centerWidth+rightWidth+4 >= width {
		return truncate(left+"  "+center+"  "+right, width)
	}

	centerStart := max(leftWidth+2, (width-centerWidth)/2)
	rightStart := max(centerStart+centerWidth+2, width-rightWidth)
	return left +
		strings.Repeat(" ", centerStart-leftWidth) +
		center +
		strings.Repeat(" ", rightStart-centerStart-centerWidth) +
		right
}

func nickColor(nick string) lipgloss.Style {
	colors := []lipgloss.Style{greenStyle, blueStyle, pinkStyle, orangeStyle, accentStyle, yellowStyle}
	sum := 0
	for _, r := range nick {
		sum += int(r)
	}
	return colors[sum%len(colors)]
}

type layoutMetrics struct {
	width       int
	topMargin   int
	topHeight   int
	heroHeight  int
	bodyHeight  int
	inputWidth  int
	leftWidth   int
	centerWidth int
	rightWidth  int
}

func (m Model) layoutMetrics() layoutMetrics {
	width := max(40, m.width)
	topMargin := 0
	topHeight := 3
	inputHeight := 3
	statusHeight := 3
	heroHeight := 0
	if m.height >= 24 && width >= 90 {
		heroHeight = 8
	}
	bodyHeight := max(5, m.height-topMargin-topHeight-heroHeight-inputHeight-statusHeight)

	leftWidth := 0
	rightWidth := 0
	if width >= 90 {
		leftWidth = clamp(width/6, 28, 36)
	}
	if width >= 120 {
		rightWidth = clamp(width/5, 34, 44)
	}
	centerWidth := max(30, width-leftWidth-rightWidth)

	return layoutMetrics{
		width:       width,
		topMargin:   topMargin,
		topHeight:   topHeight,
		heroHeight:  heroHeight,
		bodyHeight:  bodyHeight,
		inputWidth:  width,
		leftWidth:   leftWidth,
		centerWidth: centerWidth,
		rightWidth:  rightWidth,
	}
}

func (m *Model) serverAddress() string {
	host := m.cfg.Server
	port := m.effectivePort()
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func (m *Model) effectivePort() int {
	if m.cfg.Port != 0 {
		return m.cfg.Port
	}
	if m.cfg.TLS {
		return 6697
	}
	return 6667
}

func (m Model) tlsLabel() string {
	if m.cfg.TLS {
		return greenStyle.Render("on")
	}
	return metaStyle.Render("off")
}

func (m *Model) nextBuffer() {
	if len(m.bufferOrder) == 0 {
		return
	}
	index := m.activeIndex()
	index = (index + 1) % len(m.bufferOrder)
	m.active = m.bufferOrder[index]
	m.unread[m.active] = 0
	m.refreshViewport()
}

func (m *Model) prevBuffer() {
	if len(m.bufferOrder) == 0 {
		return
	}
	index := m.activeIndex()
	index = (index - 1 + len(m.bufferOrder)) % len(m.bufferOrder)
	m.active = m.bufferOrder[index]
	m.unread[m.active] = 0
	m.refreshViewport()
}

func (m *Model) activeIndex() int {
	for i, buffer := range m.bufferOrder {
		if buffer == m.active {
			return i
		}
	}
	return 0
}

func (m *Model) switchBuffer(target string) {
	if number, err := strconv.Atoi(target); err == nil {
		index := number - 1
		if index >= 0 && index < len(m.bufferOrder) {
			m.active = m.bufferOrder[index]
			m.unread[m.active] = 0
			m.refreshViewport()
			return
		}
		m.addError(statusBuffer, "buffer number out of range")
		return
	}

	if _, ok := m.buffers[target]; !ok {
		m.addError(statusBuffer, "unknown buffer: "+target)
		return
	}
	m.active = target
	m.unread[m.active] = 0
	m.refreshViewport()
}

func splitCommand(value string) (string, string) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "/")
	name, rest := splitFirst(value)
	return strings.ToLower(name), rest
}

func splitFirst(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	parts := strings.Fields(value)
	first := parts[0]
	rest := strings.TrimSpace(strings.TrimPrefix(value, first))
	return first, rest
}

func parseConnect(rest string, base irc.Config) (irc.Config, error) {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return base, fmt.Errorf("usage: /connect host[:port] [nick]")
	}

	cfg := base
	host, port, err := parseHostPort(fields[0], cfg.Port)
	if err != nil {
		return cfg, err
	}
	cfg.Server = host
	cfg.Port = port
	if len(fields) > 1 {
		cfg.Nick = fields[1]
	}
	return cfg, nil
}

func parseHostPort(value string, defaultPort int) (string, int, error) {
	host := value
	port := defaultPort

	if strings.HasPrefix(value, "[") {
		parsedHost, parsedPort, err := net.SplitHostPort(value)
		if err != nil {
			return "", 0, fmt.Errorf("invalid host:port: %s", value)
		}
		host = strings.Trim(parsedHost, "[]")
		parsed, err := strconv.Atoi(parsedPort)
		if err != nil {
			return "", 0, fmt.Errorf("invalid port: %s", parsedPort)
		}
		port = parsed
	} else if strings.Count(value, ":") == 1 {
		parts := strings.Split(value, ":")
		host = parts[0]
		parsed, err := strconv.Atoi(parts[1])
		if err != nil {
			return "", 0, fmt.Errorf("invalid port: %s", parts[1])
		}
		port = parsed
	}

	if host == "" {
		return "", 0, fmt.Errorf("server host is required")
	}
	return host, port, nil
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	tail := ""
	if width > 3 {
		tail = "..."
	}
	return ansi.Truncate(value, width, tail)
}

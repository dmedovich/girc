package irc

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Server   string
	Port     int
	Nick     string
	User     string
	RealName string
	Password string
	SASLUser string
	SASLPass string
	Channel  string
	Channels []string
	TLS      bool
}

type EventKind int

const (
	EventInfo EventKind = iota
	EventMessage
	EventNotice
	EventAction
	EventError
	EventClosed
	EventPong
)

type Event struct {
	Kind   EventKind
	Buffer string
	From   string
	Text   string
	Raw    string
	Err    error
}

type Client struct {
	cfg    Config
	conn   net.Conn
	events chan Event
	out    chan string
	done   chan struct{}

	mu      sync.RWMutex
	writeMu sync.Mutex
	names   map[string][]string
	nick    string
}

func NewClient(cfg Config) *Client {
	if cfg.Port == 0 {
		if cfg.TLS {
			cfg.Port = 6697
		} else {
			cfg.Port = 6667
		}
	}
	if cfg.User == "" {
		cfg.User = cfg.Nick
	}
	if cfg.RealName == "" {
		cfg.RealName = cfg.User
	}
	if cfg.SASLPass != "" && cfg.SASLUser == "" {
		cfg.SASLUser = cfg.Nick
	}

	return &Client{
		cfg:    cfg,
		events: make(chan Event, 1024),
		out:    make(chan string, 128),
		done:   make(chan struct{}),
		names:  make(map[string][]string),
		nick:   cfg.Nick,
	}
}

func (c *Client) Connect(ctx context.Context) error {
	if c.cfg.SASLPass != "" && !c.cfg.TLS {
		return errors.New("refusing to send SASL password without TLS")
	}

	addr := fmt.Sprintf("%s:%d", c.cfg.Server, c.cfg.Port)

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}

	if c.cfg.TLS {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: c.cfg.Server, MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return err
		}
		conn = tlsConn
	}

	c.conn = conn
	go c.writeLoop()
	go c.readLoop()

	if c.cfg.SASLPass != "" {
		c.Send("CAP", "LS", "302")
	}
	if c.cfg.Password != "" {
		c.Send("PASS", c.cfg.Password)
	}
	c.Send("NICK", c.cfg.Nick)
	c.Send("USER", c.cfg.User, "0", "*", c.cfg.RealName)
	return nil
}

func (c *Client) Events() <-chan Event {
	return c.events
}

func (c *Client) Nick() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.nick
}

func (c *Client) Send(command string, params ...string) {
	c.SendRaw(formatCommand(command, params...))
}

func formatCommand(command string, params ...string) string {
	var builder strings.Builder
	builder.WriteString(strings.ToUpper(command))
	for _, param := range params {
		builder.WriteByte(' ')
		builder.WriteString(EscapeParam(param))
	}
	return builder.String()
}

func (c *Client) SendRaw(line string) {
	line = sanitizeRawLine(line)
	if line == "" {
		return
	}
	select {
	case c.out <- line:
	case <-c.done:
	}
}

func (c *Client) Join(channel string) {
	c.Send("JOIN", channel)
}

func (c *Client) Part(channel string) {
	c.Send("PART", channel)
}

func (c *Client) Privmsg(target, text string) {
	c.Send("PRIVMSG", target, sanitizeUserText(text))
}

func (c *Client) Action(target, text string) {
	c.Send("PRIVMSG", target, "\x01ACTION "+sanitizeUserText(text)+"\x01")
}

func (c *Client) NickChange(nick string) {
	c.Send("NICK", nick)
}

func (c *Client) Ping(token string) {
	c.Send("PING", token)
}

func (c *Client) Quit(message string) {
	if message == "" {
		message = "leaving"
	}
	_ = c.writeLine(formatCommand("QUIT", message))
	_ = c.Close()
}

func (c *Client) Close() error {
	select {
	case <-c.done:
		return nil
	default:
		close(c.done)
	}

	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

func (c *Client) writeLoop() {
	for {
		select {
		case line := <-c.out:
			if err := c.writeLine(line); err != nil {
				c.emit(Event{Kind: EventError, Buffer: "status", Err: err, Text: err.Error()})
				_ = c.Close()
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *Client) writeLine(line string) error {
	if c.conn == nil {
		return nil
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, err := fmt.Fprintf(c.conn, "%s\r\n", line)
	return err
}

func (c *Client) readLoop() {
	scanner := bufio.NewScanner(c.conn)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		msg := Parse(line)
		c.handle(msg)
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, net.ErrClosed) {
		c.emit(Event{Kind: EventError, Buffer: "status", Err: err, Text: err.Error()})
	}
	c.emit(Event{Kind: EventClosed, Buffer: "status", Text: "connection closed"})
	_ = c.Close()
}

func (c *Client) handle(msg Message) {
	switch msg.Command {
	case "PING":
		if len(msg.Params) > 0 {
			c.Send("PONG", msg.Params[0])
		}
	case "PONG":
		c.handlePong(msg)
	case "CAP":
		c.handleCap(msg)
	case "AUTHENTICATE":
		c.handleAuthenticate(msg)
	case "900":
		c.emitNumeric(msg)
	case "903":
		c.emit(Event{Kind: EventInfo, Buffer: "status", Text: "SASL authentication successful"})
		c.Send("CAP", "END")
	case "904", "905", "906", "907":
		c.emitNumeric(msg)
		c.Send("CAP", "END")
	case "001":
		if len(msg.Params) > 0 {
			c.setNick(msg.Params[0])
		}
		c.emit(Event{Kind: EventInfo, Buffer: "status", Text: "connected"})
		for _, channel := range c.cfg.JoinChannels() {
			c.Join(channel)
		}
	case "332":
		if len(msg.Params) >= 3 {
			c.emit(Event{Kind: EventInfo, Buffer: msg.Params[1], Text: "topic: " + msg.Params[2]})
		}
	case "353":
		if len(msg.Params) >= 4 {
			channel := msg.Params[2]
			c.names[channel] = append(c.names[channel], strings.Fields(msg.Params[3])...)
		}
	case "366":
		if len(msg.Params) >= 2 {
			c.emitNames(msg.Params[1])
		}
	case "433":
		c.emit(Event{Kind: EventError, Buffer: "status", Text: "nickname is already in use"})
	case "PRIVMSG":
		c.handlePrivmsg(msg)
	case "NOTICE":
		c.handleNotice(msg)
	case "JOIN":
		c.handleJoin(msg)
	case "PART":
		c.handlePart(msg)
	case "NICK":
		c.handleNick(msg)
	case "QUIT":
		c.handleQuit(msg)
	default:
		if isNumeric(msg.Command) {
			c.emitNumeric(msg)
		}
	}
}

func (cfg Config) JoinChannels() []string {
	seen := make(map[string]bool)
	channels := make([]string, 0, len(cfg.Channels)+1)
	for _, channel := range append([]string{cfg.Channel}, cfg.Channels...) {
		channel = strings.TrimSpace(channel)
		if channel == "" || seen[channel] {
			continue
		}
		seen[channel] = true
		channels = append(channels, channel)
	}
	return channels
}

func (c *Client) handleCap(msg Message) {
	if len(msg.Params) < 2 {
		return
	}

	subcommand := strings.ToUpper(msg.Params[1])
	capabilities := strings.ToLower(strings.Join(msg.Params[2:], " "))

	switch subcommand {
	case "LS":
		if hasCapability(capabilities, "sasl") {
			c.Send("CAP", "REQ", "sasl")
			return
		}
		c.emit(Event{Kind: EventError, Buffer: "status", Text: "server does not advertise SASL"})
		c.Send("CAP", "END")
	case "ACK":
		if hasCapability(capabilities, "sasl") {
			c.Send("AUTHENTICATE", "PLAIN")
		}
	case "NAK":
		c.emit(Event{Kind: EventError, Buffer: "status", Text: "server rejected SASL capability"})
		c.Send("CAP", "END")
	}
}

func (c *Client) handleAuthenticate(msg Message) {
	if len(msg.Params) == 0 || msg.Params[0] != "+" {
		return
	}
	for _, chunk := range saslPlainChunks(c.cfg.SASLUser, c.cfg.SASLPass) {
		c.Send("AUTHENTICATE", chunk)
	}
}

func hasCapability(capabilities, want string) bool {
	for _, capability := range strings.Fields(capabilities) {
		capability = strings.TrimPrefix(capability, ":")
		capability = strings.TrimPrefix(capability, "-")
		if capability == want || strings.HasPrefix(capability, want+"=") {
			return true
		}
	}
	return false
}

func saslPlainChunks(account, password string) []string {
	payload := base64.StdEncoding.EncodeToString([]byte("\x00" + account + "\x00" + password))
	if payload == "" {
		return []string{"+"}
	}

	const maxChunk = 400
	chunks := make([]string, 0, (len(payload)/maxChunk)+1)
	for len(payload) > maxChunk {
		chunks = append(chunks, payload[:maxChunk])
		payload = payload[maxChunk:]
	}
	chunks = append(chunks, payload)
	if len(payload) == maxChunk {
		chunks = append(chunks, "+")
	}
	return chunks
}

func (c *Client) emitNames(channel string) {
	names := c.names[channel]
	delete(c.names, channel)

	if len(names) == 0 {
		c.emit(Event{Kind: EventInfo, Buffer: channel, Text: "names: no visible users"})
		return
	}

	const sampleSize = 24
	sample := names
	suffix := ""
	if len(names) > sampleSize {
		sample = names[:sampleSize]
		suffix = fmt.Sprintf(" ... +%d more", len(names)-sampleSize)
	}

	text := fmt.Sprintf("names: %d visible users (%s%s)", len(names), strings.Join(sample, " "), suffix)
	c.emit(Event{Kind: EventInfo, Buffer: channel, Text: text})
}

func (c *Client) emitNumeric(msg Message) {
	kind := EventInfo
	if strings.HasPrefix(msg.Command, "4") || strings.HasPrefix(msg.Command, "5") {
		kind = EventError
	}

	buffer := "status"
	if len(msg.Params) > 1 && strings.HasPrefix(msg.Params[1], "#") {
		buffer = msg.Params[1]
	}

	text := formatNumericText(msg)
	if text == "" {
		text = msg.Raw
	}
	c.emit(Event{Kind: kind, Buffer: buffer, Text: text, Raw: msg.Raw})
}

func formatNumericText(msg Message) string {
	if len(msg.Params) == 0 {
		return ""
	}

	params := msg.Params
	if len(params) > 0 {
		params = params[1:]
	}
	return strings.Join(params, " ")
}

func (c *Client) handlePrivmsg(msg Message) {
	if len(msg.Params) < 2 {
		return
	}

	from := msg.Nick()
	target := msg.Params[0]
	text := msg.Params[1]
	buffer := target
	if strings.EqualFold(target, c.Nick()) {
		buffer = from
	}

	if strings.HasPrefix(text, "\x01ACTION ") && strings.HasSuffix(text, "\x01") {
		text = strings.TrimSuffix(strings.TrimPrefix(text, "\x01ACTION "), "\x01")
		c.emit(Event{Kind: EventAction, Buffer: buffer, From: from, Text: text, Raw: msg.Raw})
		return
	}

	c.emit(Event{Kind: EventMessage, Buffer: buffer, From: from, Text: text, Raw: msg.Raw})
}

func (c *Client) handleNotice(msg Message) {
	if len(msg.Params) < 2 {
		return
	}

	from := msg.Nick()
	buffer := msg.Params[0]
	if strings.EqualFold(buffer, c.Nick()) || buffer == "*" {
		buffer = "status"
	}
	c.emit(Event{Kind: EventNotice, Buffer: buffer, From: from, Text: msg.Params[1], Raw: msg.Raw})
}

func (c *Client) handlePong(msg Message) {
	token := ""
	if len(msg.Params) > 0 {
		token = msg.Params[len(msg.Params)-1]
	}
	c.emit(Event{Kind: EventPong, Buffer: "status", Text: token, Raw: msg.Raw})
}

func (c *Client) handleJoin(msg Message) {
	if len(msg.Params) < 1 {
		return
	}

	nick := msg.Nick()
	channel := msg.Params[0]
	if strings.EqualFold(nick, c.Nick()) {
		c.emit(Event{Kind: EventInfo, Buffer: channel, Text: "joined " + channel})
		return
	}
	c.emit(Event{Kind: EventInfo, Buffer: channel, Text: nick + " joined"})
}

func (c *Client) handlePart(msg Message) {
	if len(msg.Params) < 1 {
		return
	}

	nick := msg.Nick()
	channel := msg.Params[0]
	if strings.EqualFold(nick, c.Nick()) {
		c.emit(Event{Kind: EventInfo, Buffer: channel, Text: "left " + channel})
		return
	}
	c.emit(Event{Kind: EventInfo, Buffer: channel, Text: nick + " left"})
}

func (c *Client) handleNick(msg Message) {
	if len(msg.Params) < 1 {
		return
	}

	oldNick := msg.Nick()
	newNick := msg.Params[0]
	if strings.EqualFold(oldNick, c.Nick()) {
		c.setNick(newNick)
		c.emit(Event{Kind: EventInfo, Buffer: "status", Text: "you are now " + newNick})
		return
	}
	c.emit(Event{Kind: EventInfo, Buffer: "status", Text: oldNick + " is now " + newNick})
}

func (c *Client) handleQuit(msg Message) {
	nick := msg.Nick()
	if nick == "" || strings.EqualFold(nick, c.Nick()) {
		return
	}
	reason := ""
	if len(msg.Params) > 0 {
		reason = ": " + msg.Params[0]
	}
	c.emit(Event{Kind: EventInfo, Buffer: "status", Text: nick + " quit" + reason})
}

func (c *Client) setNick(nick string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nick = nick
}

func (c *Client) emit(event Event) {
	select {
	case c.events <- event:
	case <-c.done:
		if event.Kind == EventClosed {
			select {
			case c.events <- event:
			default:
			}
		}
	default:
		select {
		case <-c.events:
		default:
		}
		select {
		case c.events <- event:
		default:
		}
	}
}

func isNumeric(command string) bool {
	if len(command) != 3 {
		return false
	}
	for _, r := range command {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

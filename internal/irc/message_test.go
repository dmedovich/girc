package irc

import (
	"context"
	"strings"
	"testing"
)

func TestParsePrivmsg(t *testing.T) {
	msg := Parse(":nick!user@example.test PRIVMSG #girc :hello there")

	if msg.Prefix != "nick!user@example.test" {
		t.Fatalf("prefix = %q", msg.Prefix)
	}
	if msg.Nick() != "nick" {
		t.Fatalf("nick = %q", msg.Nick())
	}
	if msg.Command != "PRIVMSG" {
		t.Fatalf("command = %q", msg.Command)
	}
	if len(msg.Params) != 2 || msg.Params[0] != "#girc" || msg.Params[1] != "hello there" {
		t.Fatalf("params = %#v", msg.Params)
	}
}

func TestParsePing(t *testing.T) {
	msg := Parse("PING :irc.example.test")

	if msg.Command != "PING" {
		t.Fatalf("command = %q", msg.Command)
	}
	if len(msg.Params) != 1 || msg.Params[0] != "irc.example.test" {
		t.Fatalf("params = %#v", msg.Params)
	}
}

func TestEscapeParam(t *testing.T) {
	tests := map[string]string{
		"":            ":",
		"hello":       "hello",
		"hello there": ":hello there",
		":already":    ":already",
	}

	for input, want := range tests {
		if got := EscapeParam(input); got != want {
			t.Fatalf("EscapeParam(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestEscapeParamStripsWireControls(t *testing.T) {
	got := EscapeParam("hello\r\nOPER root secret\x00")
	if strings.ContainsAny(got, "\r\n\x00") {
		t.Fatalf("escaped param contains wire controls: %q", got)
	}
	if got != ":helloOPER root secret" {
		t.Fatalf("escaped param = %q", got)
	}
}

func TestSanitizeRawLineStripsControlCharacters(t *testing.T) {
	got := sanitizeRawLine("JOIN #zig\r\nOPER root secret\x1b[31m")
	if strings.ContainsAny(got, "\r\n\x1b") {
		t.Fatalf("raw line contains controls: %q", got)
	}
	if got != "JOIN #zigOPER root secret[31m" {
		t.Fatalf("raw line = %q", got)
	}
}

func TestClientRefusesSASLWithoutTLS(t *testing.T) {
	client := NewClient(Config{Server: "irc.example.test", Nick: "me", SASLPass: "secret", TLS: false})

	err := client.Connect(context.Background())
	if err == nil {
		t.Fatal("expected plaintext SASL refusal")
	}
	if !strings.Contains(err.Error(), "without TLS") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientCompactsNamesList(t *testing.T) {
	client := NewClient(Config{Nick: "me"})

	client.handle(Parse(":irc.example.test 353 me = #linux :me alice bob carol"))
	client.handle(Parse(":irc.example.test 353 me = #linux :dave erin frank"))
	client.handle(Parse(":irc.example.test 366 me #linux :End of /NAMES list."))

	event := <-client.Events()
	if event.Buffer != "#linux" {
		t.Fatalf("buffer = %q", event.Buffer)
	}
	if event.Text != "names: 7 visible users (me alice bob carol dave erin frank)" {
		t.Fatalf("text = %q", event.Text)
	}
}

func TestClientRoutesChannelErrors(t *testing.T) {
	client := NewClient(Config{Nick: "me"})

	client.handle(Parse(":irc.example.test 404 me #linux :Cannot send to channel"))

	event := <-client.Events()
	if event.Kind != EventError {
		t.Fatalf("kind = %v", event.Kind)
	}
	if event.Buffer != "#linux" {
		t.Fatalf("buffer = %q", event.Buffer)
	}
	if event.Text != "#linux Cannot send to channel" {
		t.Fatalf("text = %q", event.Text)
	}
}

func TestConfigJoinChannelsDeduplicatesLegacyAndList(t *testing.T) {
	cfg := Config{
		Channel:  "#zig",
		Channels: []string{"#zig", "#lobsters", "##rust", "#go-nuts"},
	}
	got := cfg.JoinChannels()
	want := []string{"#zig", "#lobsters", "##rust", "#go-nuts"}

	if len(got) != len(want) {
		t.Fatalf("JoinChannels length = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("JoinChannels[%d] = %q, want %q: %v", i, got[i], want[i], got)
		}
	}
}

func TestSASLPlainChunks(t *testing.T) {
	chunks := saslPlainChunks("myNick", "secret")

	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d", len(chunks))
	}
	if chunks[0] != "AG15TmljawBzZWNyZXQ=" {
		t.Fatalf("chunk = %q", chunks[0])
	}
}

func TestClientEmitsPongToken(t *testing.T) {
	client := NewClient(Config{Nick: "me"})

	client.handle(Parse(":irc.example.test PONG irc.example.test :girc-token"))

	event := <-client.Events()
	if event.Kind != EventPong {
		t.Fatalf("kind = %v", event.Kind)
	}
	if event.Text != "girc-token" {
		t.Fatalf("text = %q", event.Text)
	}
}

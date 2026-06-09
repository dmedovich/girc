package irc

import "strings"

type Message struct {
	Raw     string
	Prefix  string
	Command string
	Params  []string
}

func Parse(line string) Message {
	line = strings.TrimRight(line, "\r\n")
	msg := Message{Raw: line}
	rest := line

	if strings.HasPrefix(rest, "@") {
		if i := strings.IndexByte(rest, ' '); i >= 0 {
			rest = strings.TrimLeft(rest[i+1:], " ")
		}
	}

	if strings.HasPrefix(rest, ":") {
		if i := strings.IndexByte(rest, ' '); i >= 0 {
			msg.Prefix = rest[1:i]
			rest = strings.TrimLeft(rest[i+1:], " ")
		}
	}

	if i := strings.IndexByte(rest, ' '); i >= 0 {
		msg.Command = strings.ToUpper(rest[:i])
		rest = strings.TrimLeft(rest[i+1:], " ")
	} else {
		msg.Command = strings.ToUpper(rest)
		return msg
	}

	for rest != "" {
		if strings.HasPrefix(rest, ":") {
			msg.Params = append(msg.Params, rest[1:])
			break
		}

		next := strings.IndexByte(rest, ' ')
		if next < 0 {
			msg.Params = append(msg.Params, rest)
			break
		}

		msg.Params = append(msg.Params, rest[:next])
		rest = strings.TrimLeft(rest[next+1:], " ")
	}

	return msg
}

func (m Message) Nick() string {
	nick := m.Prefix
	if i := strings.IndexAny(nick, "!@"); i >= 0 {
		return nick[:i]
	}
	return nick
}

func EscapeParam(param string) string {
	param = sanitizeWireParam(param)
	if param == "" || strings.ContainsAny(param, " \t") || strings.HasPrefix(param, ":") {
		return ":" + strings.TrimPrefix(param, ":")
	}
	return param
}

func sanitizeWireParam(param string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\x01':
			return r
		case '\r', '\n':
			return -1
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, param)
}

func sanitizeRawLine(line string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, line))
}

func sanitizeUserText(text string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, text)
}

package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"girc/internal/irc"
	"girc/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	if err := loadDotEnvUpwards(); err != nil {
		fmt.Fprintf(os.Stderr, "girc: %v\n", err)
	}

	var cfg irc.Config
	var plain bool
	var channels string

	flag.StringVar(&cfg.Server, "server", envString("GIRC_SERVER", "irc.libera.chat"), "IRC server hostname")
	flag.IntVar(&cfg.Port, "port", envInt("GIRC_PORT", 6697), "IRC server port")
	flag.StringVar(&cfg.Nick, "nick", envString("GIRC_NICK", defaultNick()), "IRC nickname")
	flag.StringVar(&cfg.User, "user", envString("GIRC_USER", "girc"), "IRC username")
	flag.StringVar(&cfg.RealName, "realname", envString("GIRC_REALNAME", "girc TUI"), "IRC real name")
	flag.StringVar(&cfg.Password, "pass", "", "server password")
	flag.StringVar(&cfg.SASLUser, "sasl-user", os.Getenv("GIRC_SASL_USER"), "NickServ account name for SASL login")
	flag.StringVar(&cfg.SASLPass, "sasl-pass", "", "NickServ password for SASL login")
	flag.StringVar(&cfg.Channel, "channel", os.Getenv("GIRC_CHANNEL"), "channel to join after connecting")
	flag.StringVar(&channels, "channels", os.Getenv("GIRC_CHANNELS"), "comma/space-separated channels to join after connecting")
	flag.BoolVar(&plain, "plain", envBool("GIRC_PLAIN", false), "disable TLS")
	flag.Parse()

	if cfg.Password == "" {
		cfg.Password = os.Getenv("GIRC_PASS")
	}
	if cfg.SASLPass == "" {
		cfg.SASLPass = os.Getenv("GIRC_SASL_PASS")
	}
	if cfg.Channel != "" && !strings.HasPrefix(cfg.Channel, "#") {
		cfg.Channel = "#" + cfg.Channel
	}
	cfg.Channels = parseChannels(channels)
	if cfg.Channel == "" && len(cfg.Channels) > 0 {
		cfg.Channel = cfg.Channels[0]
	}
	cfg.TLS = !plain

	program := tea.NewProgram(tui.New(cfg), tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "girc: %v\n", err)
		os.Exit(1)
	}
}

func parseChannels(value string) []string {
	value = strings.ReplaceAll(value, ",", " ")
	fields := strings.Fields(value)
	channels := make([]string, 0, len(fields))
	seen := make(map[string]bool)
	for _, field := range fields {
		channel := normalizeChannel(field)
		if channel == "" || seen[channel] {
			continue
		}
		seen[channel] = true
		channels = append(channels, channel)
	}
	return channels
}

func normalizeChannel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "#") {
		return value
	}
	return "#" + value
}

func defaultNick() string {
	source := rand.New(rand.NewSource(time.Now().UnixNano()))
	return fmt.Sprintf("girc%d", source.Intn(9000)+1000)
}

func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "girc: invalid %s=%q, using %d\n", name, value, fallback)
		return fallback
	}
	return parsed
}

func envBool(name string, fallback bool) bool {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "girc: invalid %s=%q, using %t\n", name, value, fallback)
		return fallback
	}
	return parsed
}

func loadDotEnvUpwards() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}

	for {
		path := filepath.Join(dir, ".env")
		if _, err := os.Stat(path); err == nil {
			return loadDotEnv(path)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

func loadDotEnv(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("could not read %s: %w", path, err)
	}

	for lineNumber, line := range strings.Split(string(data), "\n") {
		key, value, ok, err := parseDotEnvLine(line)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", path, lineNumber+1, err)
		}
		if !ok || os.Getenv(key) != "" {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("%s:%d: %w", path, lineNumber+1, err)
		}
	}

	return nil
}

func parseDotEnvLine(line string) (string, string, bool, error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false, nil
	}

	line = strings.TrimPrefix(line, "export ")
	key, value, ok := strings.Cut(line, "=")
	if !ok {
		return "", "", false, fmt.Errorf("expected KEY=value")
	}

	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || !isEnvKey(key) {
		return "", "", false, fmt.Errorf("invalid env key %q", key)
	}

	if len(value) >= 2 {
		quote := value[0]
		if (quote == '\'' || quote == '"') && value[len(value)-1] == quote {
			value = value[1 : len(value)-1]
		}
	}

	return key, value, true, nil
}

func isEnvKey(key string) bool {
	for i, r := range key {
		if r == '_' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' && i > 0 {
			continue
		}
		return false
	}
	return true
}

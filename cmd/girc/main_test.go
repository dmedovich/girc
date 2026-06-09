package main

import "testing"

func TestParseDotEnvLine(t *testing.T) {
	tests := []struct {
		line      string
		wantKey   string
		wantValue string
		wantOK    bool
	}{
		{"", "", "", false},
		{"# comment", "", "", false},
		{"GIRC_NICK=dmedovich", "GIRC_NICK", "dmedovich", true},
		{"GIRC_CHANNEL=#linux", "GIRC_CHANNEL", "#linux", true},
		{"export GIRC_SASL_PASS='secret value'", "GIRC_SASL_PASS", "secret value", true},
	}

	for _, test := range tests {
		key, value, ok, err := parseDotEnvLine(test.line)
		if err != nil {
			t.Fatalf("parseDotEnvLine(%q) returned error: %v", test.line, err)
		}
		if key != test.wantKey || value != test.wantValue || ok != test.wantOK {
			t.Fatalf("parseDotEnvLine(%q) = %q, %q, %t; want %q, %q, %t", test.line, key, value, ok, test.wantKey, test.wantValue, test.wantOK)
		}
	}
}

func TestParseDotEnvLineRejectsInvalidLine(t *testing.T) {
	_, _, _, err := parseDotEnvLine("not-a-setting")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseChannels(t *testing.T) {
	got := parseChannels("#zig, lobsters ##rust go-nuts #zig")
	want := []string{"#zig", "#lobsters", "##rust", "#go-nuts"}

	if len(got) != len(want) {
		t.Fatalf("parseChannels length = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseChannels[%d] = %q, want %q: %v", i, got[i], want[i], got)
		}
	}
}

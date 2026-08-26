package logredact

import (
	"bytes"
	"strings"
	"testing"
)

func TestConfiguredSecretsRedacted(t *testing.T) {
	var buf bytes.Buffer
	w := Writer(&buf, "supersecretvalue", "glpat-abcdef123456")
	line := "posting with token supersecretvalue and pat glpat-abcdef123456 done\n"
	n, err := w.Write([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if n != len(line) {
		t.Errorf("Write reported %d, want original length %d", n, len(line))
	}
	out := buf.String()
	if strings.Contains(out, "supersecretvalue") || strings.Contains(out, "glpat-abcdef123456") {
		t.Errorf("secret leaked: %q", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("no redaction marker: %q", out)
	}
}

func TestTokenShapesRedactedEvenWhenUnknown(t *testing.T) {
	var buf bytes.Buffer
	w := Writer(&buf) // no configured secrets at all
	_, _ = w.Write([]byte("error echoing trimci_agent_XYZ12345_qqqq and glrt-runnertoken here"))
	out := buf.String()
	if strings.Contains(out, "trimci_agent_XYZ12345") || strings.Contains(out, "glrt-runnertoken") {
		t.Errorf("shaped token leaked: %q", out)
	}
}

func TestShortSecretsIgnored(t *testing.T) {
	var buf bytes.Buffer
	w := Writer(&buf, "abc") // too short to be a real secret — must not redact
	_, _ = w.Write([]byte("abcabc"))
	if buf.String() != "abcabc" {
		t.Errorf("short pattern was redacted: %q", buf.String())
	}
}

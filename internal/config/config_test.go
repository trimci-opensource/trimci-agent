package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setValid populates the minimal valid environment.
func setValid(t *testing.T) {
	t.Helper()
	t.Setenv("TRIMCI_TOKEN", "trimci_agent_AAAAAAAA_secretsecretsecret")
	t.Setenv("GITLAB_URL", "https://gitlab.internal.example.com")
	t.Setenv("GITLAB_TOKEN", "glpat-abcdefghijklmnop")
	// Explicitly clear knobs the host environment might carry.
	for _, name := range []string{
		"TRIMCI_URL", "TRIMCI_TOKEN_FILE", "GITLAB_TOKEN_FILE", "GITLAB_CA_BUNDLE",
		"LOG_LEVEL", "LOG_FORMAT", "HEALTHZ_ADDR", "TRIMCI_ALLOW_HTTP",
	} {
		t.Setenv(name, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	setValid(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.TrimCIURL != "https://trimci.com" {
		t.Errorf("TrimCIURL = %q", cfg.TrimCIURL)
	}
	if cfg.LogLevel != "info" || cfg.LogFormat != "text" {
		t.Errorf("log defaults = %q/%q", cfg.LogLevel, cfg.LogFormat)
	}
	if cfg.AllowInsecureHTTP || cfg.HealthzAddr != "" {
		t.Error("insecure/healthz should default off")
	}
}

func TestTrailingSlashesTrimmed(t *testing.T) {
	setValid(t)
	t.Setenv("TRIMCI_URL", "https://sandbox.trimci.com/")
	t.Setenv("GITLAB_URL", "https://gitlab.example.com/")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if strings.HasSuffix(cfg.TrimCIURL, "/") || strings.HasSuffix(cfg.GitLabURL, "/") {
		t.Errorf("slashes kept: %q %q", cfg.TrimCIURL, cfg.GitLabURL)
	}
}

func TestMissingRequired(t *testing.T) {
	setValid(t)
	t.Setenv("TRIMCI_TOKEN", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TRIMCI_TOKEN") {
		t.Errorf("expected TRIMCI_TOKEN error, got %v", err)
	}

	setValid(t)
	t.Setenv("GITLAB_URL", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "GITLAB_URL") {
		t.Errorf("expected GITLAB_URL error, got %v", err)
	}
}

func TestTokenPrefixEnforced(t *testing.T) {
	setValid(t)
	t.Setenv("TRIMCI_TOKEN", "not-a-trimci-token")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), TokenPrefix) {
		t.Errorf("expected prefix error, got %v", err)
	}
}

func TestHTTPRefusedUnlessAllowed(t *testing.T) {
	setValid(t)
	t.Setenv("TRIMCI_URL", "http://localhost:8000")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "https") {
		t.Errorf("expected https error, got %v", err)
	}
	t.Setenv("TRIMCI_ALLOW_HTTP", "true")
	if _, err := Load(); err != nil {
		t.Errorf("TRIMCI_ALLOW_HTTP should permit http, got %v", err)
	}
}

func TestGitLabHTTPAllowed(t *testing.T) {
	setValid(t)
	t.Setenv("GITLAB_URL", "http://gitlab.local")
	if _, err := Load(); err != nil {
		t.Errorf("plain-http GitLab must be allowed, got %v", err)
	}
}

func TestTokenFile(t *testing.T) {
	setValid(t)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("trimci_agent_BBBBBBBB_filesecret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRIMCI_TOKEN", "")
	t.Setenv("TRIMCI_TOKEN_FILE", path)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.TrimCIToken != "trimci_agent_BBBBBBBB_filesecret" {
		t.Errorf("token from file = %q (newline must be trimmed)", cfg.TrimCIToken)
	}
}

func TestBadLogSettings(t *testing.T) {
	setValid(t)
	t.Setenv("LOG_FORMAT", "yaml")
	if _, err := Load(); err == nil {
		t.Error("expected LOG_FORMAT error")
	}
	setValid(t)
	t.Setenv("LOG_LEVEL", "loud")
	if _, err := Load(); err == nil {
		t.Error("expected LOG_LEVEL error")
	}
}

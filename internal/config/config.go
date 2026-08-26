// Package config loads the agent's configuration from environment variables.
//
// Env vars are the ONLY configuration surface — everything behavioral
// (poll interval, batch caps, per-repo cursors, log requests) is driven by
// the server through the hello response, so a config file would have nothing
// to say. The variable names here are a public contract: the TrimCI
// dashboard prints ready-to-run docker commands using exactly these names.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// TokenPrefix is the fixed prefix of every TrimCI agent token. Used for a
// fail-fast mis-paste check and by log redaction.
const TokenPrefix = "trimci_agent_"

// Config is the fully validated agent configuration.
type Config struct {
	// TrimCIURL is the ingest API origin (default https://trimci.com).
	TrimCIURL string
	// TrimCIToken is the agent token minted in the TrimCI dashboard.
	TrimCIToken string
	// GitLabURL is the base URL of the customer's GitLab instance.
	GitLabURL string
	// GitLabToken is the customer's read_api access token. It is only ever
	// sent to GitLabURL — never to TrimCI.
	GitLabToken string
	// GitLabCABundle is an optional PEM file with extra CA certificates for
	// the GitLab side (internal CAs). Appended to the system pool.
	GitLabCABundle string
	// LogLevel is one of debug|info|warn|error (default info).
	LogLevel string
	// LogFormat is text|json (default text).
	LogFormat string
	// HealthzAddr, when non-empty, serves GET /healthz on that address
	// (e.g. "127.0.0.1:8080"). Empty = no listening ports at all.
	HealthzAddr string
	// AllowInsecureHTTP permits an http:// TrimCIURL. Development only —
	// never set this against a production TrimCI.
	AllowInsecureHTTP bool
}

// Load reads and validates the configuration from the process environment.
func Load() (Config, error) {
	cfg := Config{
		TrimCIURL:         strings.TrimRight(envDefault("TRIMCI_URL", "https://trimci.com"), "/"),
		GitLabURL:         strings.TrimRight(os.Getenv("GITLAB_URL"), "/"),
		GitLabCABundle:    os.Getenv("GITLAB_CA_BUNDLE"),
		LogLevel:          strings.ToLower(envDefault("LOG_LEVEL", "info")),
		LogFormat:         strings.ToLower(envDefault("LOG_FORMAT", "text")),
		HealthzAddr:       os.Getenv("HEALTHZ_ADDR"),
		AllowInsecureHTTP: isTruthy(os.Getenv("TRIMCI_ALLOW_HTTP")),
	}

	var err error
	if cfg.TrimCIToken, err = secret("TRIMCI_TOKEN"); err != nil {
		return Config{}, err
	}
	if cfg.GitLabToken, err = secret("GITLAB_TOKEN"); err != nil {
		return Config{}, err
	}

	if !strings.HasPrefix(cfg.TrimCIToken, TokenPrefix) {
		return Config{}, fmt.Errorf(
			"TRIMCI_TOKEN does not start with %q — paste the full token shown once in the TrimCI dashboard", TokenPrefix)
	}

	if err := validateURL("TRIMCI_URL", cfg.TrimCIURL, cfg.AllowInsecureHTTP); err != nil {
		return Config{}, err
	}
	if cfg.GitLabURL == "" {
		return Config{}, errors.New("GITLAB_URL is required (the base URL of your GitLab instance)")
	}
	// http:// GitLab URLs are allowed (plain-HTTP internal instances exist);
	// the TLS requirement protects the leg that leaves the customer network.
	if err := validateURL("GITLAB_URL", cfg.GitLabURL, true); err != nil {
		return Config{}, err
	}

	switch cfg.LogFormat {
	case "text", "json":
	default:
		return Config{}, fmt.Errorf("LOG_FORMAT must be text or json, got %q", cfg.LogFormat)
	}
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("LOG_LEVEL must be debug, info, warn or error, got %q", cfg.LogLevel)
	}
	return cfg, nil
}

// secret resolves NAME or NAME_FILE (file contents win over nothing, the
// direct variable wins over the file). Whitespace is trimmed so a token
// file with a trailing newline — the way every secret manager writes them —
// just works.
func secret(name string) (string, error) {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value, nil
	}
	if path := os.Getenv(name + "_FILE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("reading %s_FILE: %w", name, err)
		}
		if value := strings.TrimSpace(string(raw)); value != "" {
			return value, nil
		}
		return "", fmt.Errorf("%s_FILE %q is empty", name, path)
	}
	return "", fmt.Errorf("%s (or %s_FILE) is required", name, name)
}

func validateURL(name, raw string, allowHTTP bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("%s %q is not a valid URL", name, raw)
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if allowHTTP {
			return nil
		}
		return fmt.Errorf(
			"%s must use https:// (got %q). TRIMCI_ALLOW_HTTP=true overrides this for development only", name, raw)
	default:
		return fmt.Errorf("%s %q must be an http(s) URL", name, raw)
	}
}

func envDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func isTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

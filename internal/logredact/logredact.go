// Package logredact keeps credentials out of the agent's log stream.
//
// The redaction sits at the io.Writer level, underneath the slog handler:
// every emitted line — message, attributes, wrapped errors, anything — is
// scrubbed before it reaches stdout/stderr. This catches secrets that leak
// through error strings from libraries, which per-field redaction cannot.
// slog handlers write one whole line per Write call, so a secret can never
// straddle two writes.
package logredact

import (
	"bytes"
	"io"
	"regexp"
	"sync"
)

const placeholder = "[REDACTED]"

// tokenShapes matches credential formats even when the concrete value is not
// known to the process (e.g. a second token pasted into an env var by
// mistake and echoed in an error). TrimCI agent tokens plus the GitLab token
// prefixes, mirroring the server-side log_redactor discipline.
var tokenShapes = regexp.MustCompile(
	`(trimci_agent_|glpat-|glptt-|glsoat-|gldt-|gloas-|glrt-|glcbt-)[A-Za-z0-9_\-]+`,
)

// Writer wraps w so that any occurrence of the given secrets (and any string
// matching a known credential shape) is replaced before writing.
func Writer(w io.Writer, secrets ...string) io.Writer {
	kept := make([][]byte, 0, len(secrets))
	for _, s := range secrets {
		if len(s) >= 8 { // never build a redactor that matches trivia
			kept = append(kept, []byte(s))
		}
	}
	return &redactingWriter{inner: w, secrets: kept}
}

type redactingWriter struct {
	mu      sync.Mutex
	inner   io.Writer
	secrets [][]byte
}

func (r *redactingWriter) Write(p []byte) (int, error) {
	clean := p
	for _, secret := range r.secrets {
		clean = bytes.ReplaceAll(clean, secret, []byte(placeholder))
	}
	clean = tokenShapes.ReplaceAll(clean, []byte(placeholder))
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.inner.Write(clean); err != nil {
		return 0, err
	}
	// Report the ORIGINAL length: io.Writer contracts count consumed input
	// bytes, and redaction changes the output length.
	return len(p), nil
}

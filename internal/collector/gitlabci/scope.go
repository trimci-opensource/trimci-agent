// Zero-code-access scope guard — the security heart of this agent.
//
// GitLab's read_api scope technically permits repository contents, merge
// requests, commits and more. This agent enforces a much narrower boundary
// at its single HTTP chokepoint: every outbound GitLab request is checked
// against the allowlist below BEFORE the bytes leave the process. Here is,
// verifiably, every URL this agent can request.
//
// The list is a deliberate SUBSET of the allowlist in TrimCI's server-side
// GitLab client (connectors/services/gitlab_client.py): /api/v4/user,
// /api/v4/personal_access_tokens/self and the bare single-job endpoint are
// dropped because the agent has no use for them. Anyone diffing the two
// lists: the difference is intentional, not an omission.
package gitlabci

import (
	"fmt"
	"regexp"
)

var allowedPaths = []*regexp.Regexp{
	// Instance version — reachability probe + the version shown in the dashboard.
	regexp.MustCompile(`^/api/v4/version$`),
	// Project catalog (id + path only; the request always narrows with
	// membership=true and simple=true).
	regexp.MustCompile(`^/api/v4/projects$`),
	regexp.MustCompile(`^/api/v4/projects/\d+$`),
	// Pipeline runs and the jobs within one pipeline.
	regexp.MustCompile(`^/api/v4/projects/\d+/pipelines$`),
	regexp.MustCompile(`^/api/v4/projects/\d+/pipelines/\d+$`),
	regexp.MustCompile(`^/api/v4/projects/\d+/pipelines/\d+/jobs$`),
	// Failed-job log text (the tail of which feeds failure analysis).
	regexp.MustCompile(`^/api/v4/projects/\d+/jobs/\d+/trace$`),
}

// enforceScope refuses any URL path outside the allowlist, before any HTTP
// request is made. There are no call sites that bypass it — all requests
// funnel through client.do.
func enforceScope(path string) error {
	for _, pattern := range allowedPaths {
		if pattern.MatchString(path) {
			return nil
		}
	}
	return fmt.Errorf("refusing to call %q: outside the zero-code-access scope", path)
}

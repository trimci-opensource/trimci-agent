// Package version carries the agent's build version.
//
// Version is stamped at release time via
// -ldflags "-X github.com/trimci-opensource/trimci-agent/internal/version.Version=1.2.3"
// (see .goreleaser.yaml). Development builds report "0.0.0-dev".
package version

// Version is the agent's semantic version, without a "v" prefix.
var Version = "0.0.0-dev"

// UserAgent is sent as both the User-Agent and X-TrimCI-Agent header value.
func UserAgent() string {
	return "trimci-agent/" + Version
}

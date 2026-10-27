// Package release holds what a build stamps into the agent binary.
package release

// Version is set at build time with
// -ldflags "-X zimam/agent/internal/release.Version=<version>".
// Development builds report 0.0.0-dev.
var Version = "0.0.0-dev"

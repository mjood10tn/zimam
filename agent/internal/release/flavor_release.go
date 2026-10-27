//go:build !e2e

package release

import "zimam/agent/contract"

// Flavor is the build flavour agent.version reports.
const Flavor = contract.BuildRelease

//go:build e2e

package release

import "zimam/agent/contract"

// Flavor is the build flavour agent.version reports. Only e2e builds serve
// E2EOnly commands (D22).
const Flavor = contract.BuildE2E

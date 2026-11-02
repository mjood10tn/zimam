// Command tiny stands in for the agent in the check-binary tests. It links
// the release package, so -X and the e2e tag act on it as on the agent.
package main

import (
	"fmt"

	"zimam/agent/internal/release"
)

func main() {
	fmt.Println(release.Version, release.Flavor, release.PublicKey)
}

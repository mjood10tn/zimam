package jobs

import "strconv"

// Limits are a job unit's resource limits (§5.7).
type Limits struct {
	MemoryMax     string // systemd's MemoryMax=
	RuntimeMaxSec int
}

// limits copies the §5.7 table.
var limits = map[string]Limits{
	"php.install":    {MemoryMax: "1G", RuntimeMaxSec: 900},
	"php.uninstall":  {MemoryMax: "1G", RuntimeMaxSec: 900},
	"site.provision": {MemoryMax: "512M", RuntimeMaxSec: 300},
	"site.php":       {MemoryMax: "512M", RuntimeMaxSec: 300},
	"site.security":  {MemoryMax: "512M", RuntimeMaxSec: 300},
	"site.destroy":   {MemoryMax: "512M", RuntimeMaxSec: 300},
	"cert.issue":     {MemoryMax: "512M", RuntimeMaxSec: 300},
	"security.check": {MemoryMax: "512M", RuntimeMaxSec: 600},
	"php.default":    {MemoryMax: "256M", RuntimeMaxSec: 120},
	"logs.policy":    {MemoryMax: "256M", RuntimeMaxSec: 120},
}

// LimitsFor returns a job command's limits. Every job command has them; a
// missing entry is a programming error, which the tests catch.
func LimitsFor(command string) (Limits, bool) {
	l, ok := limits[command]
	return l, ok
}

// properties are the unit properties systemd-run gets.
func (l Limits) properties() []string {
	return []string{"Type=exec", "MemoryMax=" + l.MemoryMax, "RuntimeMaxSec=" + strconv.Itoa(l.RuntimeMaxSec)}
}

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
	// v0.2.0's job commands (the plan's "New agent commands" table).
	"site.repo":      {MemoryMax: "512M", RuntimeMaxSec: 600},
	"site.deploy":    {MemoryMax: "2G", RuntimeMaxSec: 1800},
	"site.rollback":  {MemoryMax: "512M", RuntimeMaxSec: 300},
	"site.env.write": {MemoryMax: "256M", RuntimeMaxSec: 120},
	// v0.3.0 database and cache commands.
	"mysql.install":  {MemoryMax: "1G", RuntimeMaxSec: 900},
	"db.create":      {MemoryMax: "256M", RuntimeMaxSec: 120},
	"db.drop":        {MemoryMax: "256M", RuntimeMaxSec: 300},
	"db.user":        {MemoryMax: "256M", RuntimeMaxSec: 120},
	"valkey.install": {MemoryMax: "512M", RuntimeMaxSec: 900},
	"cache.user":     {MemoryMax: "256M", RuntimeMaxSec: 120},
	// v0.4.0 worker, cron and Node commands (the plan's "New agent commands" table).
	"site.workers":         {MemoryMax: "256M", RuntimeMaxSec: 300},
	"site.workers.restart": {MemoryMax: "128M", RuntimeMaxSec: 300},
	"site.cron":            {MemoryMax: "128M", RuntimeMaxSec: 120},
	"node.install":         {MemoryMax: "1G", RuntimeMaxSec: 900},
	"node.uninstall":       {MemoryMax: "512M", RuntimeMaxSec: 600},
	// v0.5.0 backup commands (the plan's "New agent commands" table; OQ5).
	"backup.destination":        {MemoryMax: "256M", RuntimeMaxSec: 300},
	"backup.destination.delete": {MemoryMax: "128M", RuntimeMaxSec: 120},
	"backup.destination.test":   {MemoryMax: "256M", RuntimeMaxSec: 300},
	"backup.plan":               {MemoryMax: "128M", RuntimeMaxSec: 120},
	"backup.plan.delete":        {MemoryMax: "128M", RuntimeMaxSec: 120},
	"backup.run":                {MemoryMax: "1G", RuntimeMaxSec: 21600},
	"backup.restore":            {MemoryMax: "1G", RuntimeMaxSec: 21600},
}

// LimitsFor returns a job command's limits. Every job command has them; a
// missing entry is a programming error, which the tests catch.
func LimitsFor(command string) (Limits, bool) {
	l, ok := limits[command]
	return l, ok
}

// properties are the unit properties systemd-run gets. MemorySwapMax=0 makes
// MemoryMax a hard limit: on a server with swap, a job over its limit would
// otherwise page instead of being killed (oom-kill).
func (l Limits) properties() []string {
	return []string{
		"Type=exec",
		"MemoryMax=" + l.MemoryMax,
		"MemorySwapMax=0",
		"RuntimeMaxSec=" + strconv.Itoa(l.RuntimeMaxSec),
	}
}

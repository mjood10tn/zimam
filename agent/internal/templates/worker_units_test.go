package templates

import (
	"bytes"
	"strings"
	"testing"
)

// The worker unit takes its sandbox from the shared part, rendered with the
// site's home as the part's one input (R04-D20), so v0.7's panel units can
// include the same set.
func TestSandboxPartSharedByWorkerUnit(t *testing.T) {
	part, err := Render("systemd/sandbox.part", "/home/s_shop")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"NoNewPrivileges=yes", "ProtectSystem=strict", "ProtectHome=tmpfs", "BindPaths=/home/s_shop", "ReadWritePaths=/home/s_shop", "UMask=0027"} {
		if !bytes.Contains(part, []byte(d+"\n")) {
			t.Errorf("the sandbox part lacks %s:\n%s", d, part)
		}
	}
	unit, err := Render("systemd/zimam-worker.service", WorkerUnit{Site: "shop", ID: "queue", MemoryMB: 512, TimeoutStopSec: 90})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(unit, part) {
		t.Errorf("the worker unit doesn't hold the sandbox part as rendered:\n%s", unit)
	}
	// A home that isn't safe is refused, not written into a unit.
	if _, err := Render("systemd/sandbox.part", "/home/s_shop /etc"); err == nil {
		t.Error("the part rendered a home with a space")
	}
}

// A crash loop never reaches "failed" on systemd's default start limit
// (spike, R04-D8): the unit carries one that RestartSec=5 does reach.
func TestWorkerUnitHasStartLimit(t *testing.T) {
	unit, err := Render("systemd/zimam-worker.service", WorkerUnit{Site: "shop", ID: "queue", MemoryMB: 512, TimeoutStopSec: 90})
	if err != nil {
		t.Fatal(err)
	}
	unitSection, _, _ := strings.Cut(string(unit), "[Service]")
	for _, d := range []string{"StartLimitIntervalSec=600\n", "StartLimitBurst=10\n", "PartOf=zimam-site-shop.target\n"} {
		if !strings.Contains(unitSection, d) {
			t.Errorf("[Unit] lacks %s", d)
		}
	}
}

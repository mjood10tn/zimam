package contract

import "testing"

func TestUpdatesValues(t *testing.T) {
	checkValues(t,
		[]ClockTime{"00:00", "03:30", "23:59"},
		[]ClockTime{"", "3:30", "24:00", "03:60", "03:30 ", "03:30; id", "now"})
	checkValues(t,
		[]UpdatesScope{"security", "all"},
		[]UpdatesScope{"", "dist", "Security", "all "})
	checkValues(t,
		[]RebootAt{"now", "00:00", "23:59"},
		[]RebootAt{"", "25:00", "+5", "03:00; id", "Now", "03:00 "})
}

func TestUpdatesAutoParams(t *testing.T) {
	for _, p := range []UpdatesAutoParams{{}, {Enabled: true}, {Enabled: true, AutoReboot: true, RebootTime: "03:30"}, {RebootTime: "04:00"}} {
		if err := p.Validate(); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
	}
	// TestAutoRebootNeedsTime.
	for _, p := range []UpdatesAutoParams{{Enabled: true, AutoReboot: true}, {Enabled: true, AutoReboot: true, RebootTime: "3:30"}} {
		if p.Validate() == nil {
			t.Errorf("%+v accepted", p)
		}
	}
}

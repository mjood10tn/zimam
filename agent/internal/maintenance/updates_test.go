package maintenance

import (
	"os"
	"testing"
)

// apt-check prints "<updates>;<security>" on stderr (Task 1's spike, and
// ubuntu:26.04's update-notifier-common on 2026-11-01).
func TestAptCheckParse(t *testing.T) {
	for _, tc := range []struct {
		in              string
		total, security int
	}{
		{"31;0", 31, 0},
		{"5;2\n", 5, 2},
		{"0;0", 0, 0},
	} {
		total, security, err := ParseAptCheck([]byte(tc.in))
		if err != nil || total != tc.total || security != tc.security {
			t.Errorf("ParseAptCheck(%q) = %d, %d, %v; want %d, %d", tc.in, total, security, err, tc.total, tc.security)
		}
	}
	for _, in := range []string{
		"", "\n", "5", "5;", ";2", "x;2", "5;y", "-1;0", "5;-2", "5;2;1",
		" 5;2", "5;2 ", "5;2\r\n", "5;2\n\n", "E: Error: BrokenCount > 0",
		"5;6", // more security updates than updates
		"99999999999999999999;0",
	} {
		if total, security, err := ParseAptCheck([]byte(in)); err == nil {
			t.Errorf("ParseAptCheck(%q) = %d, %d; want an error", in, total, security)
		}
	}
}

func TestAptSimulateParse(t *testing.T) {
	// apt-get -s dist-upgrade, as root, in ubuntu:26.04 on 2026-11-01, with
	// five packages downgraded to their release versions first; apt-check
	// said 5;2 on the same container.
	b, err := os.ReadFile("testdata/apt-simulate.txt")
	if err != nil {
		t.Fatal(err)
	}
	if total, security := ParseAptSimulate(b); total != 5 || security != 2 {
		t.Errorf("the fixture gives %d, %d; want 5, 2", total, security)
	}

	// Lines a real run can print besides the fixture's.
	synthetic := `The following packages have been kept back:
  systemd udev
The following NEW packages will be installed:
  newpkg
Inst newpkg (1.0-1 Ubuntu:26.04/resolute-updates [amd64])
Inst tool-security [1.0] (1.1 Ubuntu:26.04/resolute-updates [amd64])
Inst libx [1] (2 Ubuntu:26.04/resolute-security [amd64]) []
Inst php8.4-cli [8.4.27-1+ubuntu26.04.1+deb.sury.org+1] (8.4.28-1+ubuntu26.04.1+deb.sury.org+1 deb.sury.org:/resolute [amd64])
Remv oldpkg [1.0]
Conf libx (2 Ubuntu:26.04/resolute-security [amd64])
Conf newpkg (1.0-1 Ubuntu:26.04/resolute-updates [amd64])
`
	if total, security := ParseAptSimulate([]byte(synthetic)); total != 4 || security != 1 {
		t.Errorf("the synthetic output gives %d, %d; want 4 Inst lines, 1 from a -security origin", total, security)
	}
	if total, security := ParseAptSimulate(nil); total != 0 || security != 0 {
		t.Errorf("no output gives %d, %d; want 0, 0", total, security)
	}
}

package sites

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"zimam/agent/internal/paths"
)

func uidEnv(t *testing.T, mark int, loginDefs string) paths.Paths {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	mkdir(t, p.StateDir())
	mkdir(t, p.ConfigDir())
	mkdir(t, filepath.Dir(p.EtcLoginDefs()))
	if mark > 0 {
		writeFile(t, p.UIDHighwater(), `{"highest": `+strconv.Itoa(mark)+"}\n")
	}
	if loginDefs != "" {
		writeFile(t, p.EtcLoginDefs(), loginDefs)
	}
	return p
}

const passwdWith1003 = "root:x:0:0:root:/root:/bin/bash\n" +
	"admin:x:1000:1000::/home/admin:/bin/bash\n" +
	"s_old:x:1003:1003::/home/s_old:/usr/sbin/nologin\n" +
	"nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin\n"

// A uid given to a site that has since gone is never handed out again.
func TestNextUIDNeverReusesARetiredUID(t *testing.T) {
	p := uidEnv(t, 1005, "")
	got, err := nextSiteUID(p, passwdWith1003)
	if err != nil || got != 1006 {
		t.Fatalf("nextSiteUID = %d, %v; want 1006", got, err)
	}
	p = uidEnv(t, 0, "")
	if got, err := nextSiteUID(p, passwdWith1003); err != nil || got != 1004 {
		t.Fatalf("without a mark nextSiteUID = %d, %v; want 1004", got, err)
	}
	if got, err := nextSiteUID(p, "root:x:0:0::/root:/bin/bash\n"); err != nil || got != 1000 {
		t.Fatalf("on an empty range nextSiteUID = %d, %v; want 1000", got, err)
	}
}

func TestNextUIDHonoursLoginDefs(t *testing.T) {
	defs := "# comment\nUID_MIN\t\t\t 2000\nUID_MAX\t\t\t 3000\n"
	p := uidEnv(t, 0, defs)
	if got, err := nextSiteUID(p, passwdWith1003); err != nil || got != 2000 {
		t.Fatalf("nextSiteUID = %d, %v; want 2000 (1003 is outside the range)", got, err)
	}
	if got, err := nextSiteUID(p, passwdWith1003+"u:x:2500:2500::/h:/bin/sh\n"); err != nil || got != 2501 {
		t.Fatalf("nextSiteUID = %d, %v; want 2501", got, err)
	}
}

func TestNextUIDRefusesPastMax(t *testing.T) {
	p := uidEnv(t, 0, "UID_MIN 1000\nUID_MAX 1003\n")
	if got, err := nextSiteUID(p, passwdWith1003); err == nil {
		t.Fatalf("nextSiteUID = %d, nil; want a refusal past UID_MAX", got)
	}
	p = uidEnv(t, 1003, "UID_MIN 1000\nUID_MAX 1003\n")
	if _, err := nextSiteUID(p, "root:x:0:0::/root:/bin/bash\n"); err == nil {
		t.Fatal("a mark at UID_MAX gave another uid")
	}
}

func TestUIDMarkIsRootOnly(t *testing.T) {
	p := uidEnv(t, 0, "")
	if err := saveUIDMark(p, 1006); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p.UIDHighwater())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mark file: %v, %v", fi, err)
	}
	if got, err := readUIDMark(p); err != nil || got != 1006 {
		t.Fatalf("readUIDMark = %d, %v", got, err)
	}
	writeFile(t, p.UIDHighwater(), "{nope")
	if _, err := readUIDMark(p); err == nil {
		t.Fatal("a corrupt mark was accepted")
	}
}

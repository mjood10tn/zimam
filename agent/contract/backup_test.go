package contract

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestBackupIDs(t *testing.T) {
	checkValues(t,
		[]DestinationID{"s3", "offsite-1", DestinationID("a" + strings.Repeat("b", 23))},
		[]DestinationID{"", "a", "1a", "-a", "a-", "A1", "a_b", "a.b", "a\n", DestinationID("a" + strings.Repeat("b", 24))})
	checkValues(t,
		[]PlanID{"nightly", "db-hourly"},
		[]PlanID{"", "x", "Nightly", "a/b", "a..b"})
}

func TestS3Endpoint(t *testing.T) {
	checkValues(t,
		[]S3Endpoint{"https://s3.eu-central-1.amazonaws.com", "https://minio.test:9000", "https://a.b:1", "https://a.b:65535", "https://xn--bcher-kva.example"},
		[]S3Endpoint{"", "http://s3.example.com", "https://s3.example.com/", "https://s3.example.com/bucket", "https://s3.example.com:0",
			"https://s3.example.com:65536", "https://s3.example.com:080", "https://user@s3.example.com", "https://192.0.2.1", "https://localhost",
			"HTTPS://s3.example.com", "https://S3.example.com", "https://s3.example.com\n", "https://s3.example.com:9000?x"})
}

func TestS3Bucket(t *testing.T) {
	checkValues(t,
		[]S3Bucket{"abc", "my-backups", "my.backups.2026", S3Bucket(strings.Repeat("b", 63))},
		[]S3Bucket{"ab", "Upper", "a..b", "a.-b", "a-.b", "-ab", "ab-", ".ab", "a_b", "192.168.1.1", S3Bucket(strings.Repeat("b", 64)), "a b", "abc\n"})
}

func TestBackupPath(t *testing.T) {
	checkValues(t,
		[]BackupPath{"", "zimam", "backups/zimam", "a_b-c/d", BackupPath(strings.Repeat("p", 128))},
		[]BackupPath{"/abs", "a/", "a//b", "../x", "a/../b", "a/.", ".", "a.b", "A", "a b", "a\n", BackupPath(strings.Repeat("p", 129))})
}

func TestS3Region(t *testing.T) {
	checkValues(t,
		[]S3Region{"us-east-1", "auto", "eu-central-003"},
		[]S3Region{"", "US", "a_b", "a b", S3Region(strings.Repeat("r", 33))})
}

func TestS3Credential(t *testing.T) {
	checkValues(t,
		[]S3Credential{"AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "0012ab.k_-=+"},
		[]S3Credential{"", "a b", "a\nb", "a#b", "a\"b", S3Credential(strings.Repeat("k", 257))})
}

func TestSFTPHost(t *testing.T) {
	checkValuesShape(t,
		[]SFTPHost{"backup.example.com", "192.0.2.10", "2001:db8::1"},
		[]SFTPHost{"", "localhost", "Backup.example.com", "2001:DB8::1", "2001:db8:0::1", "fe80::1%eth0", "::ffff:192.0.2.1", "a b", "-oProxyCommand=x", "host\n"})
}

func TestSFTPUser(t *testing.T) {
	checkValues(t,
		[]SFTPUser{"backup", "_svc", "u-1"},
		[]SFTPUser{"", "Root", "1u", "-u", "a b", "a@b", SFTPUser("u" + strings.Repeat("x", 32))})
}

func TestTCPPort(t *testing.T) {
	checkValues(t, []TCPPort{1, 22, 65535}, []TCPPort{0, -1, 65536})
}

func TestHostKeyFingerprint(t *testing.T) {
	checkValues(t,
		[]HostKeyFingerprint{"SHA256:3qlt+g58jaxbjKRA7vJ1IWAMa/bu1tj0P9oalXbtTyw"},
		[]HostKeyFingerprint{"", "3qlt+g58jaxbjKRA7vJ1IWAMa/bu1tj0P9oalXbtTyw", "MD5:16:27:ac:a5:76:28:2d:36:63:1b:56:4d:eb:df:a6:48",
			"SHA256:3qlt+g58jaxbjKRA7vJ1IWAMa/bu1tj0P9oalXbtTy", "SHA256:3qlt+g58jaxbjKRA7vJ1IWAMa/bu1tj0P9oalXbtTyw\n"})
}

func TestEncryptionSecret(t *testing.T) {
	checkValues(t,
		[]EncryptionSecret{EncryptionSecret(strings.Repeat("A", 43)), "q9Zx_-0123456789abcdefghijABCDEFGHIJklmnopq"},
		[]EncryptionSecret{"", EncryptionSecret(strings.Repeat("A", 42)), EncryptionSecret(strings.Repeat("A", 44)), EncryptionSecret(strings.Repeat("A", 42) + "="), EncryptionSecret(strings.Repeat("A", 42) + "+")})
}

func TestBackupSchedule(t *testing.T) {
	checkValues(t,
		[]BackupSchedule{"hourly", "daily 00:00", "daily 03:30", "daily 23:59", "weekly mon 03:30", "weekly sun 23:05"},
		[]BackupSchedule{"", "daily", "daily 25:00", "daily 24:00", "daily 3:30", "daily 03:60", "weekly 03:30", "weekly Mon 03:30",
			"weekly monday 03:30", "monthly 1 03:30", "Hourly", "hourly\n", "daily 03:30 ", "*-*-* 03:30:00"})
}

func TestBackupScheduleOnCalendar(t *testing.T) {
	for in, want := range map[BackupSchedule]string{
		"hourly":           "hourly",
		"daily 03:30":      "*-*-* 03:30:00",
		"weekly mon 03:30": "Mon *-*-* 03:30:00",
		"weekly sun 23:05": "Sun *-*-* 23:05:00",
	} {
		if got := in.OnCalendar(); got != want {
			t.Errorf("%q.OnCalendar() = %q, want %q", in, got, want)
		}
	}
}

func TestBackupIDShape(t *testing.T) {
	checkValues(t,
		[]BackupID{"20261123T033000Z"},
		[]BackupID{"", "20261123T033000", "2026-11-23T03:30:00Z", "20261123t033000z", "../../x", "20261123T033000Z\n", "20261123T0330000Z"})
}

func TestBackupPart(t *testing.T) {
	checkValues(t,
		[]BackupPart{"panel", "db-s_shop_main", "files-shop", "files-a-b"},
		[]BackupPart{"", "db-", "files-", "db-mysql", "files-zimam", "db-S_shop", "files-Shop", "files-a-", "db-a;b", "site-shop", "panel2", "files-shop/../x"})
	if db, ok := BackupPart("db-s_shop_main").Database(); !ok || db != "s_shop_main" {
		t.Errorf("Database() = %q, %v", db, ok)
	}
	if s, ok := BackupPart("files-shop").Site(); !ok || s != "shop" {
		t.Errorf("Site() = %q, %v", s, ok)
	}
	if _, ok := BackupPart("panel").Site(); ok {
		t.Error("panel has a site")
	}
}

func TestRetention(t *testing.T) {
	for r, ok := range map[Retention]bool{
		{KeepLast: 1}: true,
		{KeepLast: 365, KeepDaily: 365, KeepWeekly: 365, KeepMonthly: 365}: true,
		{KeepLast: 7, KeepDaily: 0}:                                        true,
		{KeepLast: 0}:                                                      false,
		{KeepLast: 366}:                                                    false,
		{KeepLast: 1, KeepDaily: -1}:                                       false,
		{KeepLast: 1, KeepWeekly: 366}:                                     false,
		{KeepLast: 1, KeepMonthly: 366}:                                    false,
	} {
		if err := r.Validate(); (err == nil) != ok {
			t.Errorf("%+v: Validate = %v, want ok %v", r, err, ok)
		}
		checkSchemaAgrees(t, r, ok)
	}
}

func s3Dest() BackupDestinationParams {
	return BackupDestinationParams{
		ID: "offsite", Type: DestinationS3,
		S3: &S3Settings{Endpoint: "https://s3.example.com", Region: "us-east-1", Bucket: "backups", Prefix: "zimam",
			AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		EncryptionPassword: EncryptionSecret(strings.Repeat("p", 43)),
		EncryptionSalt:     EncryptionSecret(strings.Repeat("s", 43)),
	}
}

func sftpDest() BackupDestinationParams {
	d := s3Dest()
	d.Type, d.S3 = DestinationSFTP, nil
	d.SFTP = &SFTPSettings{Host: "backup.example.com", Port: 22, User: "backup", Path: "zimam"}
	return d
}

func TestBackupDestinationParams(t *testing.T) {
	withFP := sftpDest()
	withFP.SFTP.HostKeyFingerprint = "SHA256:3qlt+g58jaxbjKRA7vJ1IWAMa/bu1tj0P9oalXbtTyw"
	for _, p := range []BackupDestinationParams{s3Dest(), sftpDest(), withFP} {
		if err := p.Validate(); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
	}
	mod := func(base func() BackupDestinationParams, f func(*BackupDestinationParams)) BackupDestinationParams {
		p := base()
		f(&p)
		return p
	}
	for name, p := range map[string]BackupDestinationParams{
		"bad id":            mod(s3Dest, func(p *BackupDestinationParams) { p.ID = "X" }),
		"unknown type":      mod(s3Dest, func(p *BackupDestinationParams) { p.Type = "ftp" }),
		"s3 without fields": mod(s3Dest, func(p *BackupDestinationParams) { p.S3 = nil }),
		"s3 with sftp":      mod(s3Dest, func(p *BackupDestinationParams) { p.SFTP = sftpDest().SFTP }),
		"sftp with s3":      mod(sftpDest, func(p *BackupDestinationParams) { p.S3 = s3Dest().S3 }),
		"sftp without":      mod(sftpDest, func(p *BackupDestinationParams) { p.SFTP = nil }),
		"http endpoint":     mod(s3Dest, func(p *BackupDestinationParams) { p.S3.Endpoint = "http://s3.example.com" }),
		"endpoint path":     mod(s3Dest, func(p *BackupDestinationParams) { p.S3.Endpoint = "https://s3.example.com/x" }),
		"bucket uppercase":  mod(s3Dest, func(p *BackupDestinationParams) { p.S3.Bucket = "Backups" }),
		"bucket dots":       mod(s3Dest, func(p *BackupDestinationParams) { p.S3.Bucket = "a..b" }),
		"prefix dotdot":     mod(s3Dest, func(p *BackupDestinationParams) { p.S3.Prefix = "../x" }),
		"no access key":     mod(s3Dest, func(p *BackupDestinationParams) { p.S3.AccessKeyID = "" }),
		"key newline":       mod(s3Dest, func(p *BackupDestinationParams) { p.S3.SecretAccessKey = "a\nb" }),
		"sftp port 0":       mod(sftpDest, func(p *BackupDestinationParams) { p.SFTP.Port = 0 }),
		"sftp bad user":     mod(sftpDest, func(p *BackupDestinationParams) { p.SFTP.User = "Root" }),
		"sftp option host":  mod(sftpDest, func(p *BackupDestinationParams) { p.SFTP.Host = "-oProxyCommand=x" }),
		"md5 fingerprint": mod(sftpDest, func(p *BackupDestinationParams) {
			p.SFTP.HostKeyFingerprint = "MD5:16:27:ac:a5:76:28:2d:36:63:1b:56:4d:eb:df:a6:48"
		}),
		"short password":   mod(s3Dest, func(p *BackupDestinationParams) { p.EncryptionPassword = "abc" }),
		"no salt":          mod(s3Dest, func(p *BackupDestinationParams) { p.EncryptionSalt = "" }),
		"salt is password": mod(s3Dest, func(p *BackupDestinationParams) { p.EncryptionSalt = p.EncryptionPassword }),
	} {
		if err := p.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, p)
		}
	}
}

func nightly() BackupPlanParams {
	return BackupPlanParams{ID: "nightly", Destination: "offsite", Sites: []SiteName{"shop"}, Databases: []DatabaseName{"s_shop_main"},
		Schedule: "daily 03:30", Retention: Retention{KeepLast: 7}}
}

func TestBackupPlanParams(t *testing.T) {
	panelOnly := BackupPlanParams{ID: "panel", Destination: "offsite", Panel: true, Schedule: "hourly", Retention: Retention{KeepLast: 1}}
	for _, p := range []BackupPlanParams{nightly(), panelOnly} {
		if err := p.Validate(); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
	}
	mod := func(f func(*BackupPlanParams)) BackupPlanParams { p := nightly(); f(&p); return p }
	many := make([]SiteName, 51)
	for i := range many {
		many[i] = SiteName(fmt.Sprintf("site%d", i))
	}
	for name, p := range map[string]BackupPlanParams{
		"bad id":          mod(func(p *BackupPlanParams) { p.ID = "X" }),
		"bad destination": mod(func(p *BackupPlanParams) { p.Destination = "" }),
		"nothing":         mod(func(p *BackupPlanParams) { p.Sites, p.Databases, p.Panel = nil, nil, false }),
		"bad site":        mod(func(p *BackupPlanParams) { p.Sites = []SiteName{"Shop"} }),
		"duplicate site":  mod(func(p *BackupPlanParams) { p.Sites = []SiteName{"shop", "shop"} }),
		"too many sites":  mod(func(p *BackupPlanParams) { p.Sites = many }),
		"bad database":    mod(func(p *BackupPlanParams) { p.Databases = []DatabaseName{"mysql"} }),
		"duplicate db":    mod(func(p *BackupPlanParams) { p.Databases = []DatabaseName{"s_shop_main", "s_shop_main"} }),
		"bad schedule":    mod(func(p *BackupPlanParams) { p.Schedule = "daily 25:00" }),
		"bad retention":   mod(func(p *BackupPlanParams) { p.Retention.KeepLast = 0 }),
	} {
		if err := p.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, p)
		}
	}
}

func TestBackupRestoreParams(t *testing.T) {
	ok := []BackupRestoreParams{
		{Plan: "nightly", Backup: "20261123T033000Z", Part: "db-s_shop_main", Site: "shop", Database: "s_shop_restored"},
		{Plan: "nightly", Backup: "20261123T033000Z", Part: "db-s_shop_main", Site: "shop", Database: "s_shop_main", Overwrite: true},
		{Plan: "nightly", Backup: "20261123T033000Z", Part: "files-shop", Site: "shop"},
	}
	for _, p := range ok {
		if err := p.Validate(); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
	}
	for name, p := range map[string]BackupRestoreParams{
		"bad backup id":         {Plan: "nightly", Backup: "../../x", Part: "files-shop", Site: "shop"},
		"panel part":            {Plan: "nightly", Backup: "20261123T033000Z", Part: "panel", Site: "shop"},
		"other site's files":    {Plan: "nightly", Backup: "20261123T033000Z", Part: "files-blog", Site: "shop"},
		"files with a database": {Plan: "nightly", Backup: "20261123T033000Z", Part: "files-shop", Site: "shop", Database: "s_shop_main"},
		"files with overwrite":  {Plan: "nightly", Backup: "20261123T033000Z", Part: "files-shop", Site: "shop", Overwrite: true},
		"db without target":     {Plan: "nightly", Backup: "20261123T033000Z", Part: "db-s_shop_main", Site: "shop"},
		"db into other site":    {Plan: "nightly", Backup: "20261123T033000Z", Part: "db-s_shop_main", Site: "shop", Database: "s_blog_main"},
		"bad plan":              {Plan: "", Backup: "20261123T033000Z", Part: "files-shop", Site: "shop"},
	} {
		if err := p.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, p)
		}
	}
}

// Every secret the backup commands carry is tagged, so the audit log,
// agent_actions and job params redact it.
func TestSecretsAreTagged(t *testing.T) {
	dest, _ := Lookup("backup.destination")
	dt := reflect.TypeOf(dest.Params()).Elem()
	for _, f := range []string{"encryption_password", "encryption_salt"} {
		if !secretField(t, dt, f) {
			t.Errorf("backup.destination's %s isn't secret", f)
		}
	}
	for _, f := range []string{"access_key_id", "secret_access_key"} {
		if !secretField(t, reflect.TypeFor[S3Settings](), f) {
			t.Errorf("S3Settings.%s isn't secret", f)
		}
	}
	if secretField(t, reflect.TypeFor[S3Settings](), "bucket") {
		t.Error("the bucket is marked secret")
	}
	keys, _ := Lookup("backup.destination.keys")
	for _, f := range []string{"encryption_password", "encryption_salt"} {
		if !secretField(t, reflect.TypeOf(keys.Result), f) {
			t.Errorf("backup.destination.keys' result %s isn't secret", f)
		}
	}
	if !keys.Audited || keys.Kind != KindRead {
		t.Error("backup.destination.keys must be an audited read")
	}
}

func TestBackupSubjects(t *testing.T) {
	run, _ := Lookup("backup.run")
	if got := run.Subject(&BackupRunParams{Plan: "nightly"}); got != "backup:nightly" {
		t.Errorf("backup.run subject = %q", got)
	}
	restore, _ := Lookup("backup.restore")
	if got := restore.Subject(&BackupRestoreParams{Site: "shop"}); got != "site:shop" {
		t.Errorf("backup.restore subject = %q", got)
	}
}

// checkValuesShape is checkValues for a type whose schema is wider than
// Validate (a pattern can't parse an IP address): every valid value must
// pass the schema, and every invalid one must fail Validate.
func checkValuesShape[T validator](t *testing.T, valid, invalid []T) {
	t.Helper()
	for _, v := range valid {
		if err := v.Validate(); err != nil {
			t.Errorf("%#v: Validate() = %v, want nil", v, err)
		}
		checkSchemaShape(t, v, true)
	}
	for _, v := range invalid {
		if err := v.Validate(); err == nil {
			t.Errorf("%#v: Validate() = nil, want an error", v)
		}
	}
}

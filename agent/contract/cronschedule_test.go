package contract

import "testing"

func TestCronScheduleAccepts(t *testing.T) {
	checkAccepts(t, "*/5 * * * *", "0 3 * * mon-fri", "@daily", "@hourly", "@weekly", "@monthly", "@yearly",
		"0,30 */2 1-15/3 jan,jul sat-sun", "59 23 31 12 7", "0 0 * * 0")
}

func TestCronScheduleRefuses(t *testing.T) {
	checkRefuses(t, "@reboot", "@annually", "@daily ", "* * * * * *", "* * * *", "", "60 * * * *", "* 24 * * *",
		"* * 0 * *", "* * 32 * *", "* * * 13 *", "* * * * 8", "*/0 * * * *", "1- * * * *", "-1 * * * *",
		"1-2-3 * * * *", "5-1 * * * *", "*/5/2 * * * *", "5/2 * * * *", ", * * * *", "1,,2 * * * *",
		"* * * * *\n", "*\n* * * *", "* * * * * # x", "*/5 * * * * %", "0 3 * * 1%2", "% * * * *",
		"*  * * * *", "*\t* * * *", " * * * * *", "foo * * * *", "* * * mon *", "* * * * jan", "0 3 * * monday",
		"*/60 * * * *", "$(x) * * * *", "* * * * 1-", "1 2 3 4 5\x00",
		// U+017F folds to s under EqualFold, but the schema and cron take ASCII names only.
		"0 3 * * ſun", "0 3 * * ſat", "0 0 1 ſep *")
}

func checkAccepts(t *testing.T, in ...string) {
	t.Helper()
	for _, s := range in {
		if _, err := ParseCronSchedule(s); err != nil {
			t.Errorf("ParseCronSchedule(%q) = %v, want nil", s, err)
		}
		if err := CronSchedule(s).Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", s, err)
		}
		checkSchemaShape(t, CronSchedule(s), true)
	}
}

// checkRefuses: the schema states the shape only (names, ranges and steps
// need the parser), so it may accept what Validate refuses, never the reverse.
func checkRefuses(t *testing.T, in ...string) {
	t.Helper()
	for _, s := range in {
		if _, err := ParseCronSchedule(s); err == nil {
			t.Errorf("ParseCronSchedule(%q) = nil, want an error", s)
		}
		if err := CronSchedule(s).Validate(); err == nil {
			t.Errorf("Validate(%q) = nil, want an error", s)
		}
	}
}

func checkSchemaShape(t *testing.T, v validator, valid bool) {
	t.Helper()
	if valid && !schemaAccepts(t, jsonValue(t, v.JSONSchema()), jsonValue(t, v)) {
		t.Errorf("%#v: Validate accepts it, the schema doesn't", v)
	}
}

func TestCronScheduleNormalises(t *testing.T) {
	for in, want := range map[string]string{
		"0 3 * * MON":          "0 3 * * 1",
		"0 3 * * mon-fri":      "0 3 * * 1-5",
		"*/5 * * * *":          "*/5 * * * *",
		"0 0 1 JAN,Jul *":      "0 0 1 1,7 *",
		"0 0 * * sat-sun":      "0 0 * * 6-7",
		"0 0 * * sun-sun":      "0 0 * * 0-0",
		"0 0 * * 0-sun":        "0 0 * * 0-0",
		"0 0 * * mon-sun":      "0 0 * * 1-7",
		"0 0 * * 7":            "0 0 * * 0",
		"@daily":               "0 0 * * *",
		"@hourly":              "0 * * * *",
		"@weekly":              "0 0 * * 0",
		"@monthly":             "0 0 1 * *",
		"@yearly":              "0 0 1 1 *",
		"10-50/10 1,2,3 * * *": "10-50/10 1,2,3 * * *",
	} {
		s, err := ParseCronSchedule(in)
		if err != nil || string(s) != want || s.String() != want {
			t.Errorf("ParseCronSchedule(%q) = %q, %v; want %q", in, s, err, want)
		}
		// Normalising twice changes nothing.
		if again, err := ParseCronSchedule(want); err != nil || again != s {
			t.Errorf("normal form %q parses to %q, %v", want, again, err)
		}
	}
}

// Percent is a newline to cron, and so a second command on the line.
func TestCronScheduleNeverRendersPercent(t *testing.T) {
	if _, err := ParseCronSchedule("0 3 * * 1%touch /x"); err == nil {
		t.Error("accepted a percent sign")
	}
}

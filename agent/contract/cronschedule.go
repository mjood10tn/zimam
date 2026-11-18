package contract

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// CronSchedule is a cron job's schedule (R04-D5): five fields of `*`, `*/n`,
// `a`, `a-b`, `a-b/n` and comma lists, with month and weekday names, or one
// of the macros @hourly, @daily, @weekly, @monthly and @yearly. It never
// takes @reboot, and never a %, which cron reads as a newline. The agent
// renders a schedule only from its parsed, normalised form: names become
// numbers and macros their five fields.
type CronSchedule string

// maxCronScheduleLength is far above the longest legitimate schedule.
const maxCronScheduleLength = 200

type cronField struct {
	lo, hi int
	names  []string // name i stands for value base+i
	base   int
}

var (
	cronMonthNames = []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
	cronDayNames   = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	cronFields     = [5]cronField{
		{lo: 0, hi: 59}, {lo: 0, hi: 23}, {lo: 1, hi: 31},
		{lo: 1, hi: 12, names: cronMonthNames, base: 1},
		{lo: 0, hi: 7, names: cronDayNames}, // 0 and 7 are Sunday
	}
	cronMacros = map[string]string{
		"@hourly": "0 * * * *", "@daily": "0 0 * * *", "@weekly": "0 0 * * 0",
		"@monthly": "0 0 1 * *", "@yearly": "0 0 1 1 *",
	}
)

// ParseCronSchedule parses s strictly and returns it in normal form.
func ParseCronSchedule(s string) (CronSchedule, error) {
	if len(s) > maxCronScheduleLength {
		return "", errors.New("is too long")
	}
	if m, ok := cronMacros[s]; ok {
		return CronSchedule(m), nil
	}
	parts := strings.Split(s, " ")
	if len(parts) != 5 {
		return "", errors.New("must be five fields separated by single spaces, or one of @hourly, @daily, @weekly, @monthly and @yearly")
	}
	out := make([]string, 5)
	for i, p := range parts {
		n, err := cronFields[i].parse(p)
		if err != nil {
			return "", fmt.Errorf("field %d %v", i+1, err)
		}
		out[i] = n
	}
	return CronSchedule(strings.Join(out, " ")), nil
}

func (f cronField) parse(s string) (string, error) {
	items := strings.Split(s, ",")
	for i, item := range items {
		n, err := f.parseItem(item)
		if err != nil {
			return "", err
		}
		items[i] = n
	}
	return strings.Join(items, ","), nil
}

func (f cronField) parseItem(item string) (string, error) {
	span, step, hasStep := strings.Cut(item, "/")
	stepText := ""
	if hasStep {
		n, ok := cronNumber(step)
		if !ok || n < 1 || n > f.hi {
			return "", fmt.Errorf("has the step %q, which is not between 1 and %d", step, f.hi)
		}
		stepText = "/" + strconv.Itoa(n)
	}
	if span == "*" {
		return "*" + stepText, nil
	}
	loText, hiText, isRange := strings.Cut(span, "-")
	lo, err := f.value(loText)
	if err != nil {
		return "", err
	}
	if !isRange {
		if hasStep {
			return "", errors.New("takes a step only after * or a range")
		}
		if f.names != nil && f.hi == 7 && lo == 7 {
			lo = 0
		}
		return strconv.Itoa(lo), nil
	}
	hi, err := f.value(hiText)
	if err != nil {
		return "", err
	}
	// The name sun ends a range after Sunday as 7 (sat-sun is 6-7), but a range
	// that starts at Sunday stays Sunday-only: sun-sun is 0-0, never 0-7.
	if f.hi == 7 && hi == 0 && lo > 0 && !isCronDigits(hiText) {
		hi = 7
	}
	if lo > hi {
		return "", fmt.Errorf("has the range %q, which runs backwards", span)
	}
	return strconv.Itoa(lo) + "-" + strconv.Itoa(hi) + stepText, nil
}

// value reads a number or a name.
func (f cronField) value(s string) (int, error) {
	n, ok := cronNumber(s)
	if !ok {
		for i, name := range f.names {
			if strings.EqualFold(s, name) {
				n, ok = f.base+i, true
			}
		}
	}
	if !ok {
		return 0, fmt.Errorf("has %q, which is not a number or a name", s)
	}
	if n < f.lo || n > f.hi {
		return 0, fmt.Errorf("has %d, which is not between %d and %d", n, f.lo, f.hi)
	}
	return n, nil
}

// cronNumber reads one or two ASCII digits and nothing else (strconv.Atoi
// would take a sign).
func cronNumber(s string) (int, bool) {
	if len(s) == 0 || len(s) > 2 {
		return 0, false
	}
	n := 0
	for _, c := range []byte(s) {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

func (s CronSchedule) Validate() error {
	_, err := ParseCronSchedule(string(s))
	return err
}

// String is the normal form: what every template renders. A schedule that
// doesn't parse is returned as it is.
func (s CronSchedule) String() string {
	if n, err := ParseCronSchedule(string(s)); err == nil {
		return string(n)
	}
	return string(s)
}

// JSONSchema states the shape only: names, ranges and steps need the parser.
func (CronSchedule) JSONSchema() Schema {
	const field = `[0-9A-Za-z*/,-]+`
	return Schema{
		"type":      "string",
		"maxLength": maxCronScheduleLength,
		"pattern":   `^(@(hourly|daily|weekly|monthly|yearly)|` + field + `( ` + field + `){4})$`,
	}
}

func isCronDigits(s string) bool { _, ok := cronNumber(s); return ok }

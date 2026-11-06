package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed five-field expression: minute, hour, day of month, month, day of week. Each field takes `*`, numbers,
// ranges, lists and `/step`; months and weekdays also take their English names; 7 is Sunday too. When both day fields are
// restricted a day matches either, as in crontab. @hourly, @daily, @weekly, @monthly and @yearly stand for the usual
// expressions.
type Cron struct {
	minute, hour, dom, month, dow uint64
	domAny, dowAny                bool
}

var cronAliases = map[string]string{
	"@hourly":  "0 * * * *",
	"@daily":   "0 0 * * *",
	"@weekly":  "0 0 * * 0",
	"@monthly": "0 0 1 * *",
	"@yearly":  "0 0 1 1 *",
}

var monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
var dayNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

// ParseCron reads an expression.
func ParseCron(expr string) (*Cron, error) {
	expr = strings.TrimSpace(expr)
	if a, ok := cronAliases[strings.ToLower(expr)]; ok {
		expr = a
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, fmt.Errorf("a cron expression has five fields (minute hour day month weekday), got %d", len(f))
	}
	c := &Cron{}
	var err error
	if c.minute, _, err = cronField(f[0], 0, 59, nil); err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	if c.hour, _, err = cronField(f[1], 0, 23, nil); err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	if c.dom, c.domAny, err = cronField(f[2], 1, 31, nil); err != nil {
		return nil, fmt.Errorf("day of month: %w", err)
	}
	if c.month, _, err = cronField(f[3], 1, 12, monthNames); err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	if c.dow, c.dowAny, err = cronField(f[4], 0, 7, dayNames); err != nil {
		return nil, fmt.Errorf("day of week: %w", err)
	}
	if c.dow&(1<<7) != 0 {
		c.dow |= 1
	}
	return c, nil
}

func cronField(field string, lo, hi int, names map[string]int) (bits uint64, any bool, err error) {
	any = field == "*"
	for _, part := range strings.Split(field, ",") {
		rng, step := part, 1
		if i := strings.IndexByte(part, '/'); i >= 0 {
			rng = part[:i]
			if step, err = strconv.Atoi(part[i+1:]); err != nil || step < 1 {
				return 0, false, fmt.Errorf("bad step in %q", part)
			}
		}
		from, to := lo, hi
		if rng != "*" {
			a, b, isRange := strings.Cut(rng, "-")
			if from, err = cronValue(a, lo, hi, names); err != nil {
				return 0, false, err
			}
			to = from
			if isRange {
				if to, err = cronValue(b, lo, hi, names); err != nil {
					return 0, false, err
				}
			} else if step > 1 {
				to = hi
			}
			if to < from {
				return 0, false, fmt.Errorf("range %q runs backwards", rng)
			}
		}
		for v := from; v <= to; v += step {
			bits |= 1 << v
		}
	}
	return bits, any, nil
}

func cronValue(s string, lo, hi int, names map[string]int) (int, error) {
	if v, ok := names[strings.ToLower(s)]; ok {
		return v, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a value", s)
	}
	if v < lo || v > hi {
		return 0, fmt.Errorf("%d is outside %d-%d", v, lo, hi)
	}
	return v, nil
}

func (c *Cron) dayMatches(t time.Time) bool {
	dom := c.dom&(1<<t.Day()) != 0
	dow := c.dow&(1<<int(t.Weekday())) != 0
	if !c.domAny && !c.dowAny {
		return dom || dow
	}
	return dom && dow
}

// Next returns the first time after `after` the expression matches, in loc. It walks real minutes, so around a clock
// change a time that doesn't exist is skipped and one that happens twice matches twice. False when none comes within
// five years (February 30th).
func (c *Cron) Next(after time.Time, loc *time.Location) (time.Time, bool) {
	t := after.In(loc).Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		switch {
		case c.month&(1<<int(t.Month())) == 0:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
		case !c.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
		case c.hour&(1<<t.Hour()) == 0:
			t = t.Add(time.Duration(60-t.Minute()) * time.Minute)
		case c.minute&(1<<t.Minute()) == 0:
			t = t.Add(time.Minute)
		default:
			return t, true
		}
	}
	return time.Time{}, false
}

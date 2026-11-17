package scheduler

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"time"
	_ "time/tzdata"
)

func clock(s string) (int, error) {
	t, e := time.Parse("15:04", s)
	if e != nil {
		return 0, fmt.Errorf("use HH:MM (24-hour time): %s", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}
func (t Trigger) Validate() error {
	if t.Timezone == "" {
		t.Timezone = "Local"
	}
	if _, e := time.LoadLocation(t.Timezone); e != nil {
		return fmt.Errorf("unknown timezone %q", t.Timezone)
	}
	switch t.Kind {
	case "once":
		if t.At.IsZero() {
			return errors.New("choose a date and time")
		}
	case "interval":
		if t.EverySeconds < 1 || t.EverySeconds > 315360000 {
			return errors.New("interval must be between 1 second and 10 years")
		}
	case "random_interval":
		if t.MinSeconds < 1 || t.MaxSeconds < t.MinSeconds || t.MaxSeconds > 315360000 {
			return errors.New("random interval needs a positive minimum and maximum >= minimum")
		}
	case "daily", "weekly", "random_weekly":
		if t.Kind != "daily" {
			if len(t.Days) == 0 {
				return errors.New("select at least one weekday")
			}
			for _, d := range t.Days {
				if d < 0 || d > 6 {
					return errors.New("weekday must be 0 (Sunday) through 6 (Saturday)")
				}
			}
		}
		if t.Kind == "random_weekly" {
			a, e := clock(t.WindowStart)
			if e != nil {
				return e
			}
			b, e := clock(t.WindowEnd)
			if e != nil {
				return e
			}
			if b < a {
				return errors.New("random window must end on the same day, after its start")
			}
		} else {
			if _, e := clock(t.Time); e != nil {
				return e
			}
		}
	default:
		return errors.New("trigger must be once, interval, random_interval, daily, weekly or random_weekly")
	}
	return nil
}

// RandomFunc makes schedule calculations deterministic in tests. Its bound is inclusive.
type RandomFunc func(int64) int64

func random(n int64) int64 {
	if n <= 0 {
		return 0
	}
	v, e := rand.Int(rand.Reader, big.NewInt(n+1))
	if e != nil {
		return n / 2
	}
	return v.Int64()
}
func Next(t Trigger, after time.Time, rnd RandomFunc) (time.Time, error) {
	if e := t.Validate(); e != nil {
		return time.Time{}, e
	}
	if rnd == nil {
		rnd = random
	}
	switch t.Kind {
	case "once":
		if t.At.After(after) {
			return t.At.UTC(), nil
		}
		return time.Time{}, nil
	case "interval":
		return after.Add(time.Duration(t.EverySeconds) * time.Second).UTC(), nil
	case "random_interval":
		return after.Add(time.Duration(t.MinSeconds+rnd(t.MaxSeconds-t.MinSeconds)) * time.Second).UTC(), nil
	}
	zone := t.Timezone
	if zone == "" {
		zone = "Local"
	}
	loc, _ := time.LoadLocation(zone)
	local := after.In(loc)
	minute := 0
	if t.Kind != "random_weekly" {
		minute, _ = clock(t.Time)
	}
	for offset := 0; offset < 15; offset++ {
		day := time.Date(local.Year(), local.Month(), local.Day()+offset, 0, 0, 0, 0, loc)
		allowed := t.Kind == "daily"
		for _, d := range t.Days {
			if int(day.Weekday()) == d {
				allowed = true
			}
		}
		if !allowed {
			continue
		}
		if t.Kind == "random_weekly" {
			a, _ := clock(t.WindowStart)
			b, _ := clock(t.WindowEnd)
			// Once a window has started, skip this day rather than resampling on each wake.
			start := time.Date(day.Year(), day.Month(), day.Day(), a/60, a%60, 0, 0, loc)
			if !start.After(after) {
				continue
			}
			minute = a + int(rnd(int64(b-a)))
		}
		candidate := time.Date(day.Year(), day.Month(), day.Day(), minute/60, minute%60, 0, 0, loc)
		// Skip nonexistent wall times on daylight-saving transitions.
		if candidate.Hour()*60+candidate.Minute() != minute {
			continue
		}
		if candidate.After(after) {
			return candidate.UTC(), nil
		}
	}
	return time.Time{}, errors.New("no next occurrence found")
}

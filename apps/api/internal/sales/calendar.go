package sales

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type bizCal struct {
	loc      *time.Location
	open     time.Duration
	close    time.Duration
	weekend  map[time.Weekday]bool
	holidays map[string]bool
}

func loadCalendar(ctx context.Context, tx pgx.Tx, company uuid.UUID) (bizCal, error) {
	var zone string
	var openMin, closeMin int
	var weekend []string
	var holidays []byte
	err := tx.QueryRow(ctx, `SELECT timezone, open_minute, close_minute, weekend_days, holidays
		FROM erp.business_calendars WHERE company_id=$1`, company).Scan(&zone, &openMin, &closeMin, &weekend, &holidays)
	if err != nil {
		return bizCal{}, fmt.Errorf("sales: calendar: %w", err)
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return bizCal{}, err
	}
	cal := bizCal{
		loc: loc, open: time.Duration(openMin) * time.Minute, close: time.Duration(closeMin) * time.Minute,
		weekend: map[time.Weekday]bool{}, holidays: map[string]bool{},
	}
	for _, name := range weekend {
		if day, ok := weekdayByName(name); ok {
			cal.weekend[day] = true
		}
	}
	var items []struct {
		Date string `json:"date"`
	}
	_ = json.Unmarshal(holidays, &items)
	for _, item := range items {
		cal.holidays[item.Date] = true
	}
	return cal, nil
}

func (c bizCal) due(start time.Time, window time.Duration) (time.Time, error) {
	cur := start.In(c.loc)
	left := window
	for step := 0; left > 0 && step < 4000; step++ {
		if !c.inside(cur) {
			next, err := c.nextOpen(cur)
			if err != nil {
				return time.Time{}, err
			}
			cur = next
			continue
		}
		end := dayStart(cur, c.loc).Add(c.close)
		span := end.Sub(cur)
		if span >= left {
			return cur.Add(left), nil
		}
		left -= span
		cur = end
	}
	if left > 0 {
		return time.Time{}, fmt.Errorf("sales: no business day")
	}
	return cur, nil
}

func (c bizCal) inside(t time.Time) bool {
	t = t.In(c.loc)
	if !c.business(t) {
		return false
	}
	start := dayStart(t, c.loc)
	return !t.Before(start.Add(c.open)) && t.Before(start.Add(c.close))
}

func (c bizCal) business(t time.Time) bool {
	t = t.In(c.loc)
	if c.weekend[t.Weekday()] {
		return false
	}
	_, holiday := c.holidays[t.Format("2006-01-02")]
	return !holiday
}

func (c bizCal) nextOpen(t time.Time) (time.Time, error) {
	t = t.In(c.loc)
	d := dayStart(t, c.loc)
	if c.business(d) {
		open := d.Add(c.open)
		if t.Before(open) {
			return open, nil
		}
	}
	for i := 1; i <= 366; i++ {
		n := d.AddDate(0, 0, i)
		if c.business(n) {
			return n.Add(c.open), nil
		}
	}
	return time.Time{}, fmt.Errorf("sales: calendar did not advance")
}

func dayStart(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

func weekdayByName(name string) (time.Weekday, bool) {
	switch name {
	case "sunday":
		return time.Sunday, true
	case "monday":
		return time.Monday, true
	case "tuesday":
		return time.Tuesday, true
	case "wednesday":
		return time.Wednesday, true
	case "thursday":
		return time.Thursday, true
	case "friday":
		return time.Friday, true
	case "saturday":
		return time.Saturday, true
	default:
		return 0, false
	}
}

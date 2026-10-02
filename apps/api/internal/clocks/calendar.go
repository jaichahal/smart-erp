package clocks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/jaichahal/smart-erp/apps/api/internal/kit/apierr"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/audit"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/ifmatch"
	"github.com/jaichahal/smart-erp/apps/api/internal/kit/rls"
)

// Holiday is one day the company does not work.
type Holiday struct {
	Date string `json:"date"`
	Name string `json:"name"`
}

// Calendar is the company business-hours calendar (contract HolidayCalendar).
type Calendar struct {
	StateVersion  int64     `json:"state_version"`
	Timezone      string    `json:"timezone"`
	BusinessOpen  string    `json:"business_open"`
	BusinessClose string    `json:"business_close"`
	Weekend       []string  `json:"weekend"`
	Holidays      []Holiday `json:"holidays"`

	loc      *time.Location
	open     time.Duration
	close    time.Duration
	weekend  map[time.Weekday]bool
	holidays map[string]struct{}
}

// Service stores calendars and clocks.
type Service struct {
	pool  *pgxpool.Pool
	river *river.Client[pgx.Tx]
}

// New returns a service. river is required for ExpireDue.
func New(pool *pgxpool.Pool, riverClient *river.Client[pgx.Tx]) *Service {
	return &Service{pool: pool, river: riverClient}
}

func fail(lang string, code apierr.Code, key string) error {
	return apierr.New(code, tr(lang, key))
}

// SaveCalendar replaces the company calendar. expected is the state_version, or 0 on the first write.
func (s *Service) SaveCalendar(ctx context.Context, p rls.Principal, expected int64, in Calendar, lang string) (Calendar, error) {
	cal, err := compile(in, lang)
	if err != nil {
		return Calendar{}, err
	}
	raw, err := json.Marshal(cal.Holidays)
	if err != nil {
		return Calendar{}, err
	}
	ctx = rls.WithPrincipal(ctx, p)
	var out Calendar
	err = rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		cur, err := loadCalendar(ctx, tx, p.CompanyID, lang)
		missing := false
		if err != nil {
			var ae *apierr.Error
			if !errors.As(err, &ae) || ae.Code != apierr.NotFound {
				return err
			}
			missing = true
		}
		if missing {
			if expected != 0 {
				return ifmatch.Check(expected, 0, nil)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO erp.business_calendars
				(company_id, timezone, open_minute, close_minute, weekend_days, holidays, state_version)
				VALUES ($1, $2, $3, $4, $5, $6::jsonb, 1)`,
				p.CompanyID, cal.Timezone, int(cal.open/time.Minute), int(cal.close/time.Minute), cal.Weekend, string(raw)); err != nil {
				return fmt.Errorf("clocks: insert calendar: %w", err)
			}
		} else {
			if expected != cur.StateVersion {
				return ifmatch.Check(expected, cur.StateVersion, cur)
			}
			tag, err := tx.Exec(ctx, `UPDATE erp.business_calendars
				SET timezone = $2, open_minute = $3, close_minute = $4, weekend_days = $5, holidays = $6::jsonb, state_version = state_version + 1
				WHERE company_id = $1 AND state_version = $7`,
				p.CompanyID, cal.Timezone, int(cal.open/time.Minute), int(cal.close/time.Minute), cal.Weekend, string(raw), expected)
			if err != nil {
				return fmt.Errorf("clocks: update calendar: %w", err)
			}
			if tag.RowsAffected() == 0 {
				again, loadErr := loadCalendar(ctx, tx, p.CompanyID, lang)
				if loadErr != nil {
					return loadErr
				}
				return ifmatch.Check(expected, again.StateVersion, again)
			}
		}
		out, err = loadCalendar(ctx, tx, p.CompanyID, lang)
		if err != nil {
			return err
		}
		_, err = audit.Emit(ctx, tx, audit.Event{
			Type: "calendar.updated", ReferenceType: "holiday_calendar", ReferenceID: p.CompanyID.String(),
			Before: cur, After: out, Reason: "calendar.updated",
		})
		return err
	})
	return out, err
}

// GetCalendar returns the company calendar.
func (s *Service) GetCalendar(ctx context.Context, p rls.Principal, lang string) (Calendar, error) {
	ctx = rls.WithPrincipal(ctx, p)
	var out Calendar
	err := rls.Tx(ctx, s.pool, p, func(tx pgx.Tx) error {
		cal, err := loadCalendar(ctx, tx, p.CompanyID, lang)
		out = cal
		return err
	})
	return out, err
}

func loadCalendar(ctx context.Context, tx pgx.Tx, company uuid.UUID, lang string) (Calendar, error) {
	var cal Calendar
	var openMin, closeMin int
	var holidays []byte
	err := tx.QueryRow(ctx, `SELECT state_version, timezone, open_minute, close_minute, weekend_days, holidays
		FROM erp.business_calendars WHERE company_id = $1`, company).
		Scan(&cal.StateVersion, &cal.Timezone, &openMin, &closeMin, &cal.Weekend, &holidays)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Calendar{}, fail(lang, apierr.NotFound, "calendar_missing")
		}
		return Calendar{}, err
	}
	cal.BusinessOpen = formatMinute(openMin)
	cal.BusinessClose = formatMinute(closeMin)
	if len(holidays) > 0 {
		if err := json.Unmarshal(holidays, &cal.Holidays); err != nil {
			return Calendar{}, err
		}
	}
	return compile(cal, lang)
}

func compile(in Calendar, lang string) (Calendar, error) {
	loc, err := time.LoadLocation(in.Timezone)
	if err != nil || in.Timezone == "" {
		return Calendar{}, fail(lang, apierr.ValidationError, "calendar_invalid")
	}
	open, err := parseHHMM(in.BusinessOpen)
	if err != nil {
		return Calendar{}, fail(lang, apierr.ValidationError, "calendar_invalid")
	}
	closeAt, err := parseHHMM(in.BusinessClose)
	if err != nil || closeAt <= open {
		return Calendar{}, fail(lang, apierr.ValidationError, "calendar_invalid")
	}
	if len(in.Weekend) == 0 {
		return Calendar{}, fail(lang, apierr.ValidationError, "calendar_invalid")
	}
	weekend := map[time.Weekday]bool{}
	names := append([]string(nil), in.Weekend...)
	sort.Strings(names)
	for _, name := range names {
		day, ok := weekdayByName(name)
		if !ok {
			return Calendar{}, fail(lang, apierr.ValidationError, "calendar_invalid")
		}
		if weekend[day] {
			return Calendar{}, fail(lang, apierr.ValidationError, "calendar_invalid")
		}
		weekend[day] = true
	}
	holidays := map[string]struct{}{}
	list := append([]Holiday(nil), in.Holidays...)
	sort.Slice(list, func(i, j int) bool { return list[i].Date < list[j].Date })
	for _, h := range list {
		if _, err := time.Parse("2006-01-02", h.Date); err != nil || h.Name == "" {
			return Calendar{}, fail(lang, apierr.ValidationError, "calendar_invalid")
		}
		if _, dup := holidays[h.Date]; dup {
			return Calendar{}, fail(lang, apierr.ValidationError, "calendar_invalid")
		}
		holidays[h.Date] = struct{}{}
	}
	if list == nil {
		list = []Holiday{}
	}
	in.loc = loc
	in.open = time.Duration(open) * time.Minute
	in.close = time.Duration(closeAt) * time.Minute
	in.weekend = weekend
	in.holidays = holidays
	in.Weekend = names
	in.Holidays = list
	return in, nil
}

func parseHHMM(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, err
	}
	return t.Hour()*60 + t.Minute(), nil
}

func formatMinute(mins int) string {
	return fmt.Sprintf("%02d:%02d", mins/60, mins%60)
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

// Due returns the instant when a window of business time starting at start elapses (C5, R13.8).
func (c Calendar) Due(start time.Time, window time.Duration) (time.Time, error) {
	if c.loc == nil {
		return time.Time{}, fmt.Errorf("clocks: calendar is not compiled")
	}
	if window < 0 {
		return time.Time{}, fmt.Errorf("clocks: negative window")
	}
	cur := start.In(c.loc)
	if window == 0 {
		return cur, nil
	}
	left := window
	for step := 0; left > 0 && step < 4000; step++ {
		if !c.inside(cur) {
			next, err := c.nextOpen(cur)
			if err != nil {
				return time.Time{}, err
			}
			if !next.After(cur) {
				return time.Time{}, fmt.Errorf("clocks: calendar did not advance")
			}
			cur = next
			continue
		}
		end := dayStart(cur, c.loc).Add(c.close)
		span := end.Sub(cur)
		if span <= 0 {
			cur = end
			continue
		}
		if span >= left {
			return cur.Add(left), nil
		}
		left -= span
		cur = end
	}
	if left > 0 {
		return time.Time{}, fmt.Errorf("clocks: calendar has no working day inside the window")
	}
	return cur, nil
}

func (c Calendar) inside(t time.Time) bool {
	t = t.In(c.loc)
	if !c.isBusiness(t) {
		return false
	}
	start := dayStart(t, c.loc)
	open := start.Add(c.open)
	closeAt := start.Add(c.close)
	return !t.Before(open) && t.Before(closeAt)
}

func (c Calendar) isBusiness(t time.Time) bool {
	t = t.In(c.loc)
	if c.weekend[t.Weekday()] {
		return false
	}
	_, holiday := c.holidays[t.Format("2006-01-02")]
	return !holiday
}

func (c Calendar) nextOpen(t time.Time) (time.Time, error) {
	t = t.In(c.loc)
	d := dayStart(t, c.loc)
	if c.isBusiness(d) {
		open := d.Add(c.open)
		if t.Before(open) {
			return open, nil
		}
	}
	for i := 1; i <= 366; i++ {
		n := d.AddDate(0, 0, i)
		if c.isBusiness(n) {
			return n.Add(c.open), nil
		}
	}
	return time.Time{}, fmt.Errorf("clocks: no next working day")
}

func dayStart(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

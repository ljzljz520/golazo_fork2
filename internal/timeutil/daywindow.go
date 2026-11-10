// Package timeutil defines the natural-day window used everywhere match
// lists are attributed to calendar days: the TUI results view, the
// `finished` CLI and upcoming-fixture filtering.
//
// The problem it solves: FotMob only answers UTC calendar-date queries, while
// users think in local calendar days. Naively filtering by UTC date strings
// misattributes matches near local midnight (a 00:30 local kickoff belongs to
// "today" locally but often to "yesterday" in UTC). DayWindow makes the local
// interpretation the single source of truth:
//
//  1. A load captures one fixed clock reading and one fixed timezone, so a
//     multi-day progressive load can never drift across midnight mid-load.
//  2. The range is stored as a half-open interval [Start, End) of absolute
//     instants bounded by local midnights. Match attribution always uses
//     Contains — never UTC calendar-date strings.
//  3. UTCDates derives the minimal set of UTC calendar days that must be read
//     from the upstream API to cover the local window (a local day can span
//     two UTC dates, so an N-day window maps to N, N+1 or N+2 UTC dates).
//  4. Filtering and deduplication run against the same half-open interval,
//     keeping the TUI and the CLI byte-for-byte consistent in their ranges.
package timeutil

import (
	"time"

	"github.com/0xjuanma/golazo/internal/api"
)

// DayWindow is a half-open interval [Start, End) covering `Days` consecutive
// local calendar days, the last of which is the local day containing the
// captured Now. Instances are immutable value types safe to share across the
// progressive-load command chain.
type DayWindow struct {
	loc   *time.Location
	now   time.Time // fixed clock reading, normalized into loc
	start time.Time // local midnight of the first day in the window
	end   time.Time // local midnight immediately after the last day (exclusive)
	days  int
}

// NewDayWindow builds a window ending on the local day containing now.
// A nil location defaults to UTC; days < 1 is normalized to 1. The clock
// reading is captured here on purpose — callers must not call time.Now again
// while consuming the window.
func NewDayWindow(now time.Time, loc *time.Location, days int) DayWindow {
	if loc == nil {
		loc = time.UTC
	}
	if days < 1 {
		days = 1
	}

	localNow := now.In(loc)
	year, month, day := localNow.Date()
	todayMidnight := time.Date(year, month, day, 0, 0, 0, 0, loc)

	return DayWindow{
		loc:   loc,
		now:   localNow,
		start: todayMidnight.AddDate(0, 0, -(days - 1)),
		end:   todayMidnight.AddDate(0, 0, 1),
		days:  days,
	}
}

// Valid reports whether the window was built with NewDayWindow.
func (w DayWindow) Valid() bool { return w.loc != nil }

// Equal reports whether two windows express the same local-day range: same
// instant bounds, day count and timezone name.
func (w DayWindow) Equal(other DayWindow) bool {
	return w.days == other.days &&
		w.start.Equal(other.start) &&
		w.end.Equal(other.end) &&
		w.loc != nil && other.loc != nil &&
		w.loc.String() == other.loc.String()
}

// Now returns the fixed clock reading captured at load time.
func (w DayWindow) Now() time.Time { return w.now }

// Location returns the fixed timezone the window's local days are expressed in.
func (w DayWindow) Location() *time.Location { return w.loc }

// Start returns the inclusive lower bound as an absolute instant (local
// midnight of the first day in the window).
func (w DayWindow) Start() time.Time { return w.start }

// End returns the exclusive upper bound as an absolute instant (local
// midnight immediately after the last day in the window).
func (w DayWindow) End() time.Time { return w.end }

// Days returns the number of local calendar days covered by the window.
func (w DayWindow) Days() int { return w.days }

// Contains reports whether instant t falls inside the half-open window
// [Start, End). Comparison is on absolute instants, so the timezone attached
// to t is irrelevant — a kickoff is attributed identically regardless of how
// it is rendered.
func (w DayWindow) Contains(t time.Time) bool {
	return !t.Before(w.start) && t.Before(w.end)
}

// Today returns the one-day window covering the last local day of w. This is
// the authoritative definition of "today" for upcoming-fixture gating, so the
// 1-day results view and --include-upcoming share one boundary.
func (w DayWindow) Today() DayWindow {
	return NewDayWindow(w.now, w.loc, 1)
}

// LocalDates returns the YYYY-MM-DD labels of every local day in the window,
// ascending. Useful for debug logging and human-facing range labels.
func (w DayWindow) LocalDates() []string {
	out := make([]string, 0, w.days)
	for d := w.start; d.Before(w.end); d = d.AddDate(0, 0, 1) {
		out = append(out, d.Format("2006-01-02"))
	}
	return out
}

// IntersectsUTC reports whether the window overlaps the UTC calendar day
// starting at utcMidnight (expected to be a 00:00 UTC instant). The UTC day
// is itself half-open [utcMidnight, utcMidnight+24h); two half-open intervals
// intersect when each starts before the other ends.
func (w DayWindow) IntersectsUTC(utcMidnight time.Time) bool {
	utcDayStart := utcMidnight.UTC()
	utcDayEnd := utcDayStart.Add(24 * time.Hour)
	return utcDayStart.Before(w.end) && utcDayEnd.After(w.start)
}

// UTCDates returns the ascending UTC midnights of every UTC calendar day that
// intersects the window. These are exactly the dates the upstream API must be
// queried for; the caller still re-filters every returned match through
// Contains so attribution is decided by the local half-open interval rather
// than the UTC day bucket the API stored it in.
//
// Because End is exclusive, a UTC midnight equal to End contributes no
// matches and is omitted — this keeps a UTC-location window of N days mapped
// to exactly N UTC dates.
func (w DayWindow) UTCDates() []time.Time {
	startUTC := w.start.UTC()
	first := time.Date(startUTC.Year(), startUTC.Month(), startUTC.Day(), 0, 0, 0, 0, time.UTC)

	endUTC := w.end.UTC()
	last := time.Date(endUTC.Year(), endUTC.Month(), endUTC.Day(), 0, 0, 0, 0, time.UTC)
	if !endUTC.After(last) {
		// End lands exactly on a UTC midnight: that UTC day is outside the
		// half-open window, so drop it from the fetch plan.
		last = last.AddDate(0, 0, -1)
	}

	count := int(last.Sub(first).Hours()/24) + 1
	out := make([]time.Time, 0, count)
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		out = append(out, d)
	}
	return out
}

// UTCDateStrings returns UTCDates formatted as YYYY-MM-DD, matching the date
// key format used by the FotMob client cache and the degraded failed_dates
// envelope.
func (w DayWindow) UTCDateStrings() []string {
	dates := w.UTCDates()
	out := make([]string, 0, len(dates))
	for _, d := range dates {
		out = append(out, d.Format("2006-01-02"))
	}
	return out
}

// Filter keeps matches whose kickoff lies inside the half-open window.
// Matches without a parseable kickoff timestamp are dropped: without an
// instant there is no deterministic day attribution. Input order is
// preserved.
func Filter(matches []api.Match, w DayWindow) []api.Match {
	out := make([]api.Match, 0, len(matches))
	for _, m := range matches {
		if m.MatchTime == nil {
			continue
		}
		if w.Contains(*m.MatchTime) {
			out = append(out, m)
		}
	}
	return out
}

// FilterStatus keeps matches inside the window whose status is one of the
// supplied statuses. Input order is preserved.
func FilterStatus(matches []api.Match, w DayWindow, statuses ...api.MatchStatus) []api.Match {
	allowed := make(map[api.MatchStatus]struct{}, len(statuses))
	for _, s := range statuses {
		allowed[s] = struct{}{}
	}
	out := make([]api.Match, 0, len(matches))
	for _, m := range matches {
		if m.MatchTime == nil {
			continue
		}
		if _, ok := allowed[m.Status]; !ok {
			continue
		}
		if w.Contains(*m.MatchTime) {
			out = append(out, m)
		}
	}
	return out
}

// DedupByID collapses repeated records of the same match ID, keeping the
// first occurrence. Fetch plans read distinct UTC dates so duplicates are
// normally impossible; this remains the authoritative guard because adjacent
// UTC days can both surface fixtures near the window edges.
func DedupByID(matches []api.Match) []api.Match {
	seen := make(map[int]struct{}, len(matches))
	out := make([]api.Match, 0, len(matches))
	for _, m := range matches {
		if _, ok := seen[m.ID]; ok {
			continue
		}
		seen[m.ID] = struct{}{}
		out = append(out, m)
	}
	return out
}

// FilterDedup applies the half-open window filter and then ID deduplication,
// preserving the order of first appearance.
func FilterDedup(matches []api.Match, w DayWindow) []api.Match {
	return DedupByID(Filter(matches, w))
}

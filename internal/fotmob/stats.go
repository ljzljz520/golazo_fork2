package fotmob

import (
	"context"
	"fmt"
	"time"

	"github.com/0xjuanma/golazo/internal/api"
	"github.com/0xjuanma/golazo/internal/timeutil"
)

// StatsData holds all matches data for the stats view.
//
// Window is the fixed local-day window captured when the load started. Every
// slice below is derived from AllMatches through that same half-open
// interval, so match attribution never depends on which UTC date bucket the
// upstream API stored a fixture in.
type StatsData struct {
	// Window captures the clock reading and timezone used for this load.
	Window timeutil.DayWindow
	// AllMatches holds deduplicated matches whose kickoff is inside Window,
	// statuses preserved. It is the single cache the Today/3d/5d views filter.
	AllMatches []api.Match
	// AllFinished contains finished matches inside the full window.
	AllFinished []api.Match
	// TodayFinished contains finished matches inside the local today sub-window.
	TodayFinished []api.Match
	// TodayUpcoming contains not-yet-started matches inside local today.
	TodayUpcoming []api.Match
}

// StatsDataDays is the number of local days covered by the stats view window.
// 5 days ensures we have data even during mid-week breaks.
const StatsDataDays = 5

// StatsData fetches the stats window for the current local timezone.
// It captures the clock once; callers needing deterministic time injection
// (tests) should build a timeutil.DayWindow and call FetchStatsWindow.
func (c *Client) StatsData(ctx context.Context) (*StatsData, error) {
	window := timeutil.NewDayWindow(time.Now(), time.Local, StatsDataDays)
	return c.FetchStatsWindow(ctx, window)
}

// FetchStatsWindow executes the fetch plan derived from window:
//
//  1. The window's UTCDates decide which UTC calendar days are read. A local
//     day can span two UTC dates, so an N-day window maps to N, N+1 or N+2
//     UTC dates.
//  2. UTC dates intersecting the local today sub-window are read with both
//     fixtures and results tabs; earlier UTC dates need results only.
//  3. Returned matches are re-filtered through the same half-open local
//     interval and deduplicated by ID before the derived slices are built.
//
// An error is returned only when every UTC date failed; partial failures are
// tolerated and simply leave the missing matches absent.
func (c *Client) FetchStatsWindow(ctx context.Context, window timeutil.DayWindow) (*StatsData, error) {
	utcDates := window.UTCDates()
	today := window.Today()

	raw := make([]api.Match, 0, len(utcDates)*10)
	var lastErr error
	successCount := 0

	for _, date := range utcDates {
		// Fixtures are only needed where local today could contain upcoming
		// kickoffs; everything else is history and only has results.
		tabs := []string{"results"}
		if today.IntersectsUTC(date) {
			tabs = []string{"fixtures", "results"}
		}

		matches, err := c.MatchesByDateWithTabs(ctx, date, tabs)
		if err != nil {
			lastErr = fmt.Errorf("fetch matches for UTC date %s: %w", date.Format("2006-01-02"), err)
			continue
		}
		successCount++
		raw = append(raw, matches...)
	}

	if successCount == 0 {
		return nil, fmt.Errorf("failed to fetch matches for any UTC date: %w", lastErr)
	}

	data := &StatsData{
		Window:     window,
		AllMatches: timeutil.FilterDedup(raw, window),
	}
	data.AllFinished = timeutil.FilterStatus(data.AllMatches, window, api.MatchStatusFinished)
	data.TodayFinished = timeutil.FilterStatus(data.AllMatches, today, api.MatchStatusFinished)
	data.TodayUpcoming = timeutil.FilterStatus(data.AllMatches, today, api.MatchStatusNotStarted)
	return data, nil
}

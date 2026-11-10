package app

import (
	"testing"
	"time"

	"github.com/0xjuanma/golazo/internal/api"
	"github.com/0xjuanma/golazo/internal/fotmob"
	"github.com/0xjuanma/golazo/internal/timeutil"
	"github.com/charmbracelet/bubbles/list"
)

func statsTestModel(t *testing.T, window timeutil.DayWindow) model {
	t.Helper()
	return model{
		logger:           testLogger(),
		statsWindow:      window,
		statsDateRange:   1,
		statsMatchesList: list.New([]list.Item{}, list.NewDefaultDelegate(), 100, 20),
	}
}

func locMust(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %q: %v", name, err)
	}
	return loc
}

func ptr(t time.Time) *time.Time { return &t }

func finished(id int, t time.Time) api.Match {
	return api.Match{ID: id, Status: api.MatchStatusFinished, MatchTime: ptr(t)}
}

func upcoming(id int, t time.Time) api.Match {
	return api.Match{ID: id, Status: api.MatchStatusNotStarted, MatchTime: ptr(t)}
}

func displayedIDs(m model) map[int]bool {
	ids := make(map[int]bool, len(m.matches))
	for _, d := range m.matches {
		ids[d.ID] = true
	}
	return ids
}

// TestStatsPipeline_EastZoneMidnightAttribution drives the same derivation the
// stats handler uses, in an eastern zone where the local day spans two UTC
// dates. Midnight fixtures must follow the local day in every range view.
func TestStatsPipeline_EastZoneMidnightAttribution(t *testing.T) {
	tokyo := locMust(t, "Asia/Tokyo")
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, tokyo)
	window := timeutil.NewDayWindow(now, tokyo, fotmob.StatsDataDays)
	m := statsTestModel(t, window)
	m.statsData = &fotmob.StatsData{Window: window, AllMatches: []api.Match{
		finished(1, time.Date(2026, 6, 11, 15, 30, 0, 0, time.UTC)), // 00:30 JST today (UTC yesterday bucket)
		finished(2, time.Date(2026, 6, 12, 14, 50, 0, 0, time.UTC)), // 23:50 JST today
		finished(3, time.Date(2026, 6, 12, 15, 10, 0, 0, time.UTC)), // 00:10 JST Jun 13 -> outside window
		finished(4, time.Date(2026, 6, 10, 16, 0, 0, 0, time.UTC)),  // 01:00 JST Jun 11 (inside 5-day window)
		upcoming(5, time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)),  // 19:00 JST today
		upcoming(6, time.Date(2026, 6, 11, 14, 0, 0, 0, time.UTC)),  // 23:00 JST Jun 11 -> not local today
	}}

	m.recomputeStatsDerived()

	idsAll := map[int]bool{}
	for _, mt := range m.statsData.AllFinished {
		idsAll[mt.ID] = true
	}
	if !idsAll[1] || !idsAll[2] || !idsAll[4] {
		t.Errorf("AllFinished = %v, want 1,2,4", idsAll)
	}
	if idsAll[3] {
		t.Error("match 3 sits beyond the local window and must be excluded")
	}

	todayIDs := map[int]bool{}
	for _, mt := range m.statsData.TodayFinished {
		todayIDs[mt.ID] = true
	}
	if !todayIDs[1] || !todayIDs[2] || len(todayIDs) != 2 {
		t.Errorf("TodayFinished = %v, want exactly 1,2", todayIDs)
	}

	if len(m.statsData.TodayUpcoming) != 1 || m.statsData.TodayUpcoming[0].ID != 5 {
		t.Errorf("TodayUpcoming = %+v, want only match 5", m.statsData.TodayUpcoming)
	}
	if len(m.liveUpcomingMatches) != 1 {
		t.Errorf("live upcoming display len = %d, want 1", len(m.liveUpcomingMatches))
	}

	// Today (1-day) view: only the two finished matches attributed to local today.
	m.statsDateRange = 1
	m.applyStatsDateFilter()
	if ids := displayedIDs(m); len(ids) != 2 || !ids[1] || !ids[2] {
		t.Errorf("1-day view = %v, want matches 1,2", ids)
	}

	// 3-day view adds the Jun 11 local fixture (match 4).
	m.statsDateRange = 3
	m.applyStatsDateFilter()
	if ids := displayedIDs(m); len(ids) != 3 || !ids[4] {
		t.Errorf("3-day view = %v, want matches 1,2,4", ids)
	}

	// 5-day view: same in-window finished set, still no match 3.
	m.statsDateRange = 5
	m.applyStatsDateFilter()
	if ids := displayedIDs(m); len(ids) != 3 || ids[3] {
		t.Errorf("5-day view = %v, want matches 1,2,4 without 3", ids)
	}
}

// TestStatsPipeline_WestZoneLateEveningFixture covers a western zone: a
// 23:30 local kickoff is stored in the next UTC date but stays in today's view.
func TestStatsPipeline_WestZoneLateEveningFixture(t *testing.T) {
	la := locMust(t, "America/Los_Angeles")
	now := time.Date(2026, 6, 12, 20, 0, 0, 0, la)
	window := timeutil.NewDayWindow(now, la, 1)
	m := statsTestModel(t, window)
	m.statsData = &fotmob.StatsData{Window: window, AllMatches: []api.Match{
		finished(1, time.Date(2026, 6, 12, 7, 30, 0, 0, time.UTC)),  // 00:30 PDT
		finished(2, time.Date(2026, 6, 13, 6, 30, 0, 0, time.UTC)),  // 23:30 PDT (next UTC date)
		finished(3, time.Date(2026, 6, 13, 7, 30, 0, 0, time.UTC)),  // 00:30 PDT Jun 13 -> excluded
	}}

	m.recomputeStatsDerived()
	m.applyStatsDateFilter()

	ids := displayedIDs(m)
	if !ids[1] || !ids[2] || ids[3] || len(ids) != 2 {
		t.Errorf("west-zone today view = %v, want exactly 1,2", ids)
	}
}

// TestStatsPipeline_RangeViewsKeepFixedAnchor proves the Today/3d/5d switch
// re-derives sub-windows from the captured clock instead of calling time.Now.
// The window captured at 23:59 must keep its original local day even after the
// real clock would have crossed midnight.
func TestStatsPipeline_RangeViewsKeepFixedAnchor(t *testing.T) {
	ny := locMust(t, "America/New_York")
	captured := time.Date(2026, 11, 1, 23, 59, 0, 0, ny)
	window := timeutil.NewDayWindow(captured, ny, fotmob.StatsDataDays)
	m := statsTestModel(t, window)
	m.statsData = &fotmob.StatsData{Window: window, AllMatches: []api.Match{
		// Kickoff at 22:00 on the captured local day — stays in the 1-day view
		// regardless of how much wall time passes after the load.
		finished(1, time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)), // 22:00 EST Nov 1
	}}
	m.recomputeStatsDerived()

	for _, days := range []int{1, 3, 5, 1} {
		m.statsDateRange = days
		m.applyStatsDateFilter()
		sub := timeutil.NewDayWindow(m.statsWindow.Now(), m.statsWindow.Location(), days)
		if last := sub.LocalDates()[len(sub.LocalDates())-1]; last != "2026-11-01" {
			t.Fatalf("range %d drifted to %s; anchor must remain 2026-11-01", days, last)
		}
	}

	m.statsDateRange = 1
	m.applyStatsDateFilter()
	if ids := displayedIDs(m); len(ids) != 1 || !ids[1] {
		t.Errorf("anchored 1-day view = %v, must still contain match 1 after virtual midnight", ids)
	}
}

// TestStatsPipeline_DSTSpringForwardAttributesLocally verifies the spring-
// forward day in America/New_York: late-evening kickoffs that land on the next
// UTC date still belong to the local day.
func TestStatsPipeline_DSTSpringForwardAttributesLocally(t *testing.T) {
	ny := locMust(t, "America/New_York")
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, ny) // 23-hour local day
	window := timeutil.NewDayWindow(now, ny, 1)
	m := statsTestModel(t, window)
	m.statsData = &fotmob.StatsData{Window: window, AllMatches: []api.Match{
		finished(1, time.Date(2026, 3, 9, 3, 0, 0, 0, time.UTC)), // 23:00 EDT Mar 8 (next UTC date)
		finished(2, time.Date(2026, 3, 9, 4, 30, 0, 0, time.UTC)), // 00:30 EDT Mar 9 -> excluded
	}}

	m.recomputeStatsDerived()
	m.applyStatsDateFilter()

	ids := displayedIDs(m)
	if !ids[1] || ids[2] || len(ids) != 1 {
		t.Errorf("DST today view = %v, want only match 1", ids)
	}
}

// TestHandleStatsDayData_AccumulatesDedupsAndFilters simulates two progressive
// UTC-date batches carrying an overlapping match ID, then checks the handler
// deduplicates and attributes everything through the fixed window.
func TestHandleStatsDayData_AccumulatesDedupsAndFilters(t *testing.T) {
	tokyo := locMust(t, "Asia/Tokyo")
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, tokyo)
	window := timeutil.NewDayWindow(now, tokyo, fotmob.StatsDataDays)
	m := statsTestModel(t, window)
	m.matchDetails = &api.MatchDetails{} // suppress the batch-0 auto-detail load

	// Same fixture surfaces from both UTC date buckets (edge overlap); the
	// outside-window fixture must be dropped during accumulation.
	batch0 := statsDayDataMsg{
		batchIndex: 0,
		isLast:     false,
		utcDate:    time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC),
		matches: []api.Match{
			finished(100, time.Date(2026, 6, 11, 15, 30, 0, 0, time.UTC)), // 00:30 JST today
		},
	}
	batch1 := statsDayDataMsg{
		batchIndex: 1,
		isLast:     true,
		utcDate:    time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC),
		matches: []api.Match{
			finished(100, time.Date(2026, 6, 11, 15, 30, 0, 0, time.UTC)), // duplicate ID
			finished(101, time.Date(2026, 6, 12, 14, 50, 0, 0, time.UTC)), // 23:50 JST today
			finished(102, time.Date(2026, 6, 12, 15, 10, 0, 0, time.UTC)), // 00:10 JST tomorrow -> out
		},
	}

	updated, _ := m.handleStatsDayData(batch0)
	m = updated.(model)
	updated, _ = m.handleStatsDayData(batch1)
	m = updated.(model)

	if m.statsDaysLoaded != 2 {
		t.Errorf("statsDaysLoaded = %d, want 2", m.statsDaysLoaded)
	}
	ids := map[int]bool{}
	for _, mt := range m.statsData.AllMatches {
		ids[mt.ID] = true
	}
	if len(ids) != 2 || !ids[100] || !ids[101] {
		t.Errorf("AllMatches = %v, want deduped 100,101 only", ids)
	}
	if ids[102] {
		t.Error("outside-window match 102 must not accumulate")
	}
	if !m.statsWindow.Equal(window) {
		t.Error("handler must not mutate the fixed window")
	}
}

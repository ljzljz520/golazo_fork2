package timeutil

import (
	"testing"
	"time"

	"github.com/0xjuanma/golazo/internal/api"
)

// mustLoad loads an IANA location, failing the test if tzdata is unavailable.
func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load location %q: %v", name, err)
	}
	return loc
}

func ptrTime(t time.Time) *time.Time { return &t }

func matchAt(id int, status api.MatchStatus, t time.Time) api.Match {
	return api.Match{ID: id, Status: status, MatchTime: ptrTime(t)}
}

func TestNewDayWindow_UTCSingleDaySpansExactlyOneUTCDate(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	w := NewDayWindow(now, time.UTC, 1)

	if got := w.UTCDateStrings(); len(got) != 1 || got[0] != "2026-06-12" {
		t.Errorf("UTCDateStrings = %v, want [2026-06-12]", got)
	}
	if want := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC); !w.Start().Equal(want) {
		t.Errorf("start = %v, want %v", w.Start(), want)
	}
	if want := time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC); !w.End().Equal(want) {
		t.Errorf("end = %v, want %v", w.End(), want)
	}
}

func TestNewDayWindow_UTCMultiDayDateCount(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	for days, wantCount := range map[int]int{1: 1, 3: 3, 5: 5, 7: 7} {
		w := NewDayWindow(now, time.UTC, days)
		if got := len(w.UTCDates()); got != wantCount {
			t.Errorf("days=%d: UTC date count = %d, want %d", days, got, wantCount)
		}
		if w.Days() != days {
			t.Errorf("days=%d: Days() = %d", days, w.Days())
		}
	}
}

// TestDayWindow_EastZoneMidnightAttribution verifies an eastern zone
// (Asia/Tokyo, UTC+9): the local "today" starts at 15:00 UTC of the previous
// UTC day, so a single local day requires two UTC date reads and midnight
// fixtures stay attributed to the local day.
func TestDayWindow_EastZoneMidnightAttribution(t *testing.T) {
	tokyo := mustLoad(t, "Asia/Tokyo")
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, tokyo) // 03:00 UTC
	w := NewDayWindow(now, tokyo, 1)

	if got := w.UTCDateStrings(); len(got) != 2 || got[0] != "2026-06-11" || got[1] != "2026-06-12" {
		t.Fatalf("UTCDateStrings = %v, want [2026-06-11 2026-06-12]", got)
	}

	cases := []struct {
		name string
		kick time.Time
		want bool
	}{
		{"just after local midnight (previous UTC day)", time.Date(2026, 6, 11, 15, 30, 0, 0, time.UTC), true},  // 00:30 JST
		{"late local evening", time.Date(2026, 6, 12, 14, 50, 0, 0, time.UTC), true},                             // 23:50 JST
		{"one minute before local midnight", time.Date(2026, 6, 12, 14, 59, 0, 0, time.UTC), true},               // 23:59 JST
		{"just after window end (next local day)", time.Date(2026, 6, 12, 15, 10, 0, 0, time.UTC), false},        // 00:10 JST Jun 13
		{"just before window start (previous local day)", time.Date(2026, 6, 11, 14, 50, 0, 0, time.UTC), false}, // 23:50 JST Jun 11
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := w.Contains(tc.kick); got != tc.want {
				t.Errorf("Contains(%s) = %v, want %v", tc.kick.UTC(), got, tc.want)
			}
		})
	}
}

// TestDayWindow_WestZoneMidnightAttribution verifies a western zone
// (America/Los_Angeles, PDT UTC-7 in June): a late-evening local kickoff maps
// to the next UTC date but must remain inside the local day.
func TestDayWindow_WestZoneMidnightAttribution(t *testing.T) {
	la := mustLoad(t, "America/Los_Angeles")
	now := time.Date(2026, 6, 12, 20, 0, 0, 0, la) // 2026-06-13 03:00 UTC
	w := NewDayWindow(now, la, 1)

	if got := w.UTCDateStrings(); len(got) != 2 || got[0] != "2026-06-12" || got[1] != "2026-06-13" {
		t.Fatalf("UTCDateStrings = %v, want [2026-06-12 2026-06-13]", got)
	}

	cases := []struct {
		name string
		kick time.Time
		want bool
	}{
		{"just after local midnight", time.Date(2026, 6, 12, 7, 30, 0, 0, time.UTC), true},  // 00:30 PDT
		{"late local evening (next UTC date)", time.Date(2026, 6, 13, 6, 30, 0, 0, time.UTC), true}, // 23:30 PDT
		{"just after window end", time.Date(2026, 6, 13, 7, 30, 0, 0, time.UTC), false},     // 00:30 PDT Jun 13
		{"just before window start", time.Date(2026, 6, 12, 6, 50, 0, 0, time.UTC), false},  // 23:50 PDT Jun 11
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := w.Contains(tc.kick); got != tc.want {
				t.Errorf("Contains(%s) = %v, want %v", tc.kick.UTC(), got, tc.want)
			}
		})
	}
}

// TestDayWindow_DSTSpringForward checks the March gap in America/New_York
// (2026-03-08 02:00 EST -> 03:00 EDT): a local day lasts 23 hours and still
// maps to two UTC dates.
func TestDayWindow_DSTSpringForward(t *testing.T) {
	ny := mustLoad(t, "America/New_York")
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, ny)
	w := NewDayWindow(now, ny, 1)

	wantStart := time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC)  // 00:00 EST (-05:00)
	wantEnd := time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC)    // 00:00 EDT (-04:00)
	if !w.Start().Equal(wantStart) {
		t.Errorf("start = %v, want %v", w.Start().UTC(), wantStart)
	}
	if !w.End().Equal(wantEnd) {
		t.Errorf("end = %v, want %v", w.End().UTC(), wantEnd)
	}
	if d := w.End().Sub(w.Start()); d != 23*time.Hour {
		t.Errorf("window length = %v, want 23h", d)
	}
	if got := w.UTCDateStrings(); len(got) != 2 || got[0] != "2026-03-08" || got[1] != "2026-03-09" {
		t.Errorf("UTCDateStrings = %v, want [2026-03-08 2026-03-09]", got)
	}

	// A kickoff at the nonexistent 02:30 local wall clock is normalized by
	// Go to 03:30 EDT; window math must stay consistent on absolute instants.
	kick := time.Date(2026, 3, 8, 3, 30, 0, 0, ny)
	if !w.Contains(kick) {
		t.Errorf("kickoff %v should be inside the spring-forward day", kick)
	}
}

// TestDayWindow_DSTFallBack checks the November repeat in America/New_York
// (2026-11-01 02:00 EDT -> 01:00 EST): a local day lasts 25 hours.
func TestDayWindow_DSTFallBack(t *testing.T) {
	ny := mustLoad(t, "America/New_York")
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, ny)
	w := NewDayWindow(now, ny, 1)

	wantStart := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC) // 00:00 EDT (-04:00)
	wantEnd := time.Date(2026, 11, 2, 5, 0, 0, 0, time.UTC)   // 00:00 EST (-05:00)
	if !w.Start().Equal(wantStart) {
		t.Errorf("start = %v, want %v", w.Start().UTC(), wantStart)
	}
	if !w.End().Equal(wantEnd) {
		t.Errorf("end = %v, want %v", w.End().UTC(), wantEnd)
	}
	if d := w.End().Sub(w.Start()); d != 25*time.Hour {
		t.Errorf("window length = %v, want 25h", d)
	}
	if got := w.UTCDateStrings(); len(got) != 2 || got[0] != "2026-11-01" || got[1] != "2026-11-02" {
		t.Errorf("UTCDateStrings = %v, want [2026-11-01 2026-11-02]", got)
	}

	// A two-day window straddling fall-back spans 25h + 24h = 49h and three
	// local date labels.
	two := NewDayWindow(now, ny, 2)
	if d := two.End().Sub(two.Start()); d != 49*time.Hour {
		t.Errorf("2-day window length = %v, want 49h", d)
	}
	if got := two.LocalDates(); len(got) != 2 || got[0] != "2026-10-31" || got[1] != "2026-11-01" {
		t.Errorf("LocalDates = %v, want [2026-10-31 2026-11-01]", got)
	}
}

func TestDayWindow_HalfOpenBoundaries(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	w := NewDayWindow(now, time.UTC, 1)

	start := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC)
	if !w.Contains(start) {
		t.Error("exact start instant must be included (half-open lower bound)")
	}
	if w.Contains(end) {
		t.Error("exact end instant must be excluded (half-open upper bound)")
	}
}

func TestDayWindow_MultiDayEastAndWestDateCounts(t *testing.T) {
	tokyo := mustLoad(t, "Asia/Tokyo")
	la := mustLoad(t, "America/Los_Angeles")
	nowTokyo := time.Date(2026, 6, 12, 12, 0, 0, 0, tokyo)
	nowLA := time.Date(2026, 6, 12, 20, 0, 0, 0, la)

	// +9 zone: 5 local days span 6 UTC dates (Jun 7 .. Jun 12).
	wEast := NewDayWindow(nowTokyo, tokyo, 5)
	if got := wEast.UTCDateStrings(); len(got) != 6 || got[0] != "2026-06-07" || got[5] != "2026-06-12" {
		t.Errorf("east 5-day UTCDateStrings = %v, want 2026-06-07..2026-06-12", got)
	}
	if got := wEast.LocalDates(); len(got) != 5 {
		t.Errorf("east LocalDates len = %d, want 5", len(got))
	}

	// -7 zone: 5 local days span 6 UTC dates (Jun 8 .. Jun 13).
	wWest := NewDayWindow(nowLA, la, 5)
	if got := wWest.UTCDateStrings(); len(got) != 6 || got[0] != "2026-06-08" || got[5] != "2026-06-13" {
		t.Errorf("west 5-day UTCDateStrings = %v, want 2026-06-08..2026-06-13", got)
	}
}

func TestDayWindow_IntersectsUTC(t *testing.T) {
	tokyo := mustLoad(t, "Asia/Tokyo")
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, tokyo)
	w := NewDayWindow(now, tokyo, 1) // [Jun11 15:00Z, Jun12 15:00Z)

	utcMid := func(day int) time.Time {
		return time.Date(2026, 6, day, 0, 0, 0, 0, time.UTC)
	}
	if !w.IntersectsUTC(utcMid(11)) || !w.IntersectsUTC(utcMid(12)) {
		t.Error("UTC Jun 11 and Jun 12 must both intersect the Tokyo local day")
	}
	if w.IntersectsUTC(utcMid(10)) || w.IntersectsUTC(utcMid(13)) {
		t.Error("UTC Jun 10 and Jun 13 must not intersect the Tokyo local day")
	}
}

func TestDayWindow_TodayIsOneDaySubWindow(t *testing.T) {
	la := mustLoad(t, "America/Los_Angeles")
	now := time.Date(2026, 6, 12, 20, 0, 0, 0, la)
	w := NewDayWindow(now, la, 5)
	today := w.Today()

	if today.Days() != 1 {
		t.Fatalf("Today().Days() = %d, want 1", today.Days())
	}
	wantStart := time.Date(2026, 6, 12, 7, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 6, 13, 7, 0, 0, 0, time.UTC)
	if !today.Start().Equal(wantStart) || !today.End().Equal(wantEnd) {
		t.Errorf("today sub-window = [%v, %v), want [%v, %v)", today.Start().UTC(), today.End().UTC(), wantStart, wantEnd)
	}
}

// TestDayWindow_FixedClockPlanIsStable models a progressive multi-day load:
// the plan is derived once from a captured clock. Re-deriving from the same
// window must return the identical dates even if wall time advances, which is
// the guarantee that crossing midnight mid-load cannot change the window.
func TestDayWindow_FixedClockPlanIsStable(t *testing.T) {
	ny := mustLoad(t, "America/New_York")
	captured := time.Date(2026, 11, 1, 23, 59, 0, 0, ny) // one minute before local midnight
	w := NewDayWindow(captured, ny, 5)

	first := w.UTCDateStrings()
	// Simulate the load straddling midnight: every later batch re-derives the
	// plan from the SAME captured window instead of calling time.Now again.
	for range 5 {
		again := w.UTCDateStrings()
		if len(again) != len(first) {
			t.Fatalf("plan changed across batches: %v vs %v", again, first)
		}
		for i := range first {
			if again[i] != first[i] {
				t.Fatalf("plan changed across batches: %v vs %v", again, first)
			}
		}
	}

	// Sanity: a freshly captured window after midnight genuinely rolls, proving
	// the stability above comes from fixing the clock, not from a stale cache.
	afterMidnight := time.Date(2026, 11, 2, 0, 1, 0, 0, ny)
	rolled := NewDayWindow(afterMidnight, ny, 5)
	oldLastLocal := w.LocalDates()[len(w.LocalDates())-1]
	newLastLocal := rolled.LocalDates()[len(rolled.LocalDates())-1]
	if newLastLocal == oldLastLocal {
		t.Error("a new window after midnight should end on a new local date")
	}
}

func TestFilter_DropsNilTimestampsAndOutsideMatches(t *testing.T) {
	tokyo := mustLoad(t, "Asia/Tokyo")
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, tokyo)
	w := NewDayWindow(now, tokyo, 1)

	inside := matchAt(1, api.MatchStatusFinished, time.Date(2026, 6, 11, 16, 0, 0, 0, time.UTC))  // 01:00 JST
	outside := matchAt(2, api.MatchStatusFinished, time.Date(2026, 6, 12, 16, 0, 0, 0, time.UTC)) // 01:00 JST Jun 13
	noTime := api.Match{ID: 3, Status: api.MatchStatusFinished}

	got := Filter([]api.Match{inside, outside, noTime}, w)
	if len(got) != 1 || got[0].ID != 1 {
		t.Errorf("Filter = %+v, want only match 1", got)
	}
}

func TestFilterStatus_RespectsStatusAndWindow(t *testing.T) {
	la := mustLoad(t, "America/Los_Angeles")
	now := time.Date(2026, 6, 12, 20, 0, 0, 0, la)
	w := NewDayWindow(now, la, 1)

	finished := matchAt(1, api.MatchStatusFinished, time.Date(2026, 6, 13, 1, 0, 0, 0, time.UTC)) // 18:00 PDT
	upcoming := matchAt(2, api.MatchStatusNotStarted, time.Date(2026, 6, 13, 3, 0, 0, 0, time.UTC)) // 20:00 PDT
	outside := matchAt(3, api.MatchStatusNotStarted, time.Date(2026, 6, 13, 8, 0, 0, 0, time.UTC))  // 01:00 PDT next day

	got := FilterStatus([]api.Match{finished, upcoming, outside}, w, api.MatchStatusNotStarted)
	if len(got) != 1 || got[0].ID != 2 {
		t.Errorf("FilterStatus not_started = %+v, want only match 2", got)
	}
}

func TestDedupByID_KeepsFirstOccurrenceAndOrder(t *testing.T) {
	base := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	matches := []api.Match{
		matchAt(10, api.MatchStatusFinished, base),
		matchAt(20, api.MatchStatusFinished, base.Add(time.Hour)),
		matchAt(10, api.MatchStatusFinished, base.Add(2*time.Hour)), // duplicate ID
		matchAt(30, api.MatchStatusFinished, base.Add(3*time.Hour)),
	}
	got := DedupByID(matches)
	wantIDs := []int{10, 20, 30}
	if len(got) != len(wantIDs) {
		t.Fatalf("len = %d, want %d", len(got), len(wantIDs))
	}
	for i, id := range wantIDs {
		if got[i].ID != id {
			t.Errorf("position %d: ID = %d, want %d", i, got[i].ID, id)
		}
	}
}

func TestFilterDedup_CombinesWindowAndIDGuards(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	w := NewDayWindow(now, time.UTC, 1)
	base := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)

	matches := []api.Match{
		matchAt(1, api.MatchStatusFinished, base),
		matchAt(1, api.MatchStatusFinished, base),                    // duplicate
		matchAt(2, api.MatchStatusFinished, time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC)), // on end boundary -> excluded
	}
	got := FilterDedup(matches, w)
	if len(got) != 1 || got[0].ID != 1 {
		t.Errorf("FilterDedup = %+v, want only match 1 once", got)
	}
}

func TestNewDayWindow_NilLocationAndBadDaysNormalized(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	w := NewDayWindow(now, nil, 0)
	if w.Location() != time.UTC {
		t.Error("nil location should default to UTC")
	}
	if w.Days() != 1 {
		t.Errorf("days = %d, want clamped 1", w.Days())
	}
	if !w.Valid() {
		t.Error("window should be valid")
	}
}

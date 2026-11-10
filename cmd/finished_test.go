package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/0xjuanma/golazo/internal/api"
	"github.com/0xjuanma/golazo/internal/data"
	"github.com/0xjuanma/golazo/internal/timeutil"
)

// fakeDayFetcher records every requested UTC date and its tabs, serving
// per-date payloads and optional errors so tests can assert the fetch plan
// derived from a local window.
type fakeDayFetcher struct {
	gotTabs map[string][]string
	payload map[string][]api.Match
	errFor  map[string]error
}

func newFakeDayFetcher() *fakeDayFetcher {
	return &fakeDayFetcher{
		gotTabs: map[string][]string{},
		payload: map[string][]api.Match{},
		errFor:  map[string]error{},
	}
}

func (f *fakeDayFetcher) fetch(_ context.Context, date time.Time, tabs []string) ([]api.Match, error) {
	key := date.UTC().Format("2006-01-02")
	f.gotTabs[key] = append([]string(nil), tabs...)
	if err := f.errFor[key]; err != nil {
		return nil, err
	}
	return f.payload[key], nil
}

func finishedMatch(id int, t time.Time) api.Match {
	tt := t
	return api.Match{ID: id, Status: api.MatchStatusFinished, MatchTime: &tt}
}

func upcomingMatch(id int, t time.Time) api.Match {
	tt := t
	return api.Match{ID: id, Status: api.MatchStatusNotStarted, MatchTime: &tt}
}

func matchIDs(matches []api.Match) map[int]bool {
	ids := make(map[int]bool, len(matches))
	for _, m := range matches {
		ids[m.ID] = true
	}
	return ids
}

func mustLoadLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %q: %v", name, err)
	}
	return loc
}

func TestRunFinished_MockReturnsExpectedCount(t *testing.T) {
	t.Setenv(EnvOffline, "")
	t.Setenv(EnvAgent, "")

	var stdout, stderr bytes.Buffer
	code := runFinished(&stdout, &stderr, finishedFlags{cliFlags: cliFlags{mock: true, timeout: time.Second}, days: 1})

	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d. stderr=%s", code, ExitOK, stderr.String())
	}
	var env struct {
		Status string      `json:"status"`
		Count  int         `json:"count"`
		Data   []api.Match `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, stdout.String())
	}
	if env.Count != len(data.MockFinishedMatches()) {
		t.Errorf("count = %d, want %d", env.Count, len(data.MockFinishedMatches()))
	}
}

func TestRunFinished_InvalidDays(t *testing.T) {
	cases := []int{0, -1, 8, 100}
	for _, days := range cases {
		t.Run("", func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runFinished(&stdout, &stderr, finishedFlags{cliFlags: cliFlags{mock: true, timeout: time.Second}, days: days})
			if code != ExitInvalidArgs {
				t.Errorf("days=%d: exit = %d, want %d", days, code, ExitInvalidArgs)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout should be empty on invalid args, got: %s", stdout.String())
			}
			var env errEnvelope
			if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
				t.Fatalf("unmarshal stderr: %v", err)
			}
			if env.Code != ErrCodeInvalidArgs {
				t.Errorf("code = %q, want %q", env.Code, ErrCodeInvalidArgs)
			}
		})
	}
}

// TestCollectFinished_UTCDerivesPlanAndTabs checks the UTC baseline: an
// N-day UTC window reads exactly N UTC dates, today gets fixtures+results and
// earlier dates get results only.
func TestCollectFinished_UTCDerivesPlanAndTabs(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	window := timeutil.NewDayWindow(now, time.UTC, 2)
	f := newFakeDayFetcher()

	_, _, err := collectFinished(context.Background(), f.fetch, window, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	wantDates := []string{"2026-06-11", "2026-06-12"}
	if len(f.gotTabs) != len(wantDates) {
		t.Fatalf("fetched dates = %v, want %v", f.gotTabs, wantDates)
	}
	for _, d := range wantDates {
		tabs, ok := f.gotTabs[d]
		if !ok {
			t.Errorf("expected fetch for UTC date %s, got %v", d, f.gotTabs)
			continue
		}
		wantTabCount := 1
		if d == "2026-06-12" {
			wantTabCount = 2 // local today intersects this UTC date
		}
		if len(tabs) != wantTabCount {
			t.Errorf("tabs for %s = %v, want %d tab(s)", d, tabs, wantTabCount)
		}
	}
}

// TestCollectFinished_EastZoneMidnightAttribution verifies that in an eastern
// zone (Asia/Tokyo, UTC+9) midnight fixtures are attributed to the local day,
// including a kickoff stored under the previous UTC date, while the next local
// day's 00:10 fixture is excluded.
func TestCollectFinished_EastZoneMidnightAttribution(t *testing.T) {
	tokyo := mustLoadLoc(t, "Asia/Tokyo")
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, tokyo)
	window := timeutil.NewDayWindow(now, tokyo, 1)

	f := newFakeDayFetcher()
	f.payload["2026-06-11"] = []api.Match{
		finishedMatch(1, time.Date(2026, 6, 11, 15, 30, 0, 0, time.UTC)), // 00:30 JST today
		upcomingMatch(5, time.Date(2026, 6, 11, 14, 0, 0, 0, time.UTC)),  // 23:00 JST yesterday -> excluded
	}
	f.payload["2026-06-12"] = []api.Match{
		finishedMatch(2, time.Date(2026, 6, 12, 14, 50, 0, 0, time.UTC)), // 23:50 JST today
		finishedMatch(3, time.Date(2026, 6, 12, 15, 10, 0, 0, time.UTC)), // 00:10 JST Jun 13 -> excluded
		upcomingMatch(4, time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)),  // 19:00 JST today
	}

	matches, failed, err := collectFinished(context.Background(), f.fetch, window, true)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(failed) != 0 {
		t.Errorf("failed = %v, want empty", failed)
	}
	ids := matchIDs(matches)
	for _, want := range []int{1, 2, 4} {
		if !ids[want] {
			t.Errorf("expected match %d in local-day window, got %v", want, ids)
		}
	}
	for _, banned := range []int{3, 5} {
		if ids[banned] {
			t.Errorf("match %d must stay outside the local day window", banned)
		}
	}

	// Both UTC dates intersect local today, so both need fixtures+results.
	for d, tabs := range f.gotTabs {
		if len(tabs) != 2 {
			t.Errorf("UTC date %s tabs = %v, want fixtures+results", d, tabs)
		}
	}
}

// TestCollectFinished_WestZoneMidnightAttribution mirrors the eastern case for
// a western zone (America/Los_Angeles, PDT UTC-7): a 23:30 local kickoff lives
// in the NEXT UTC date but must remain inside the local day.
func TestCollectFinished_WestZoneMidnightAttribution(t *testing.T) {
	la := mustLoadLoc(t, "America/Los_Angeles")
	now := time.Date(2026, 6, 12, 20, 0, 0, 0, la)
	window := timeutil.NewDayWindow(now, la, 1)

	f := newFakeDayFetcher()
	f.payload["2026-06-12"] = []api.Match{
		finishedMatch(1, time.Date(2026, 6, 12, 7, 30, 0, 0, time.UTC)), // 00:30 PDT today
	}
	f.payload["2026-06-13"] = []api.Match{
		finishedMatch(2, time.Date(2026, 6, 13, 6, 30, 0, 0, time.UTC)), // 23:30 PDT today
		finishedMatch(3, time.Date(2026, 6, 13, 7, 30, 0, 0, time.UTC)), // 00:30 PDT Jun 13 -> excluded
	}

	matches, _, err := collectFinished(context.Background(), f.fetch, window, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	ids := matchIDs(matches)
	if !ids[1] || !ids[2] {
		t.Errorf("matches 1 and 2 belong to the local day, got %v", ids)
	}
	if ids[3] {
		t.Error("match 3 is past the half-open local end and must be excluded")
	}
}

// TestCollectFinished_HalfOpenBoundaries pins the exact start/end instants:
// kickoff at the start is included, kickoff at the end is excluded.
func TestCollectFinished_HalfOpenBoundaries(t *testing.T) {
	tokyo := mustLoadLoc(t, "Asia/Tokyo")
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, tokyo) // local day ends 2026-06-12 15:00Z
	window := timeutil.NewDayWindow(now, tokyo, 1)

	f := newFakeDayFetcher()
	f.payload["2026-06-11"] = []api.Match{
		finishedMatch(1, time.Date(2026, 6, 11, 15, 0, 0, 0, time.UTC)), // exactly start -> included
	}
	f.payload["2026-06-12"] = []api.Match{
		finishedMatch(2, time.Date(2026, 6, 12, 15, 0, 0, 0, time.UTC)), // exactly end -> excluded
	}

	matches, _, err := collectFinished(context.Background(), f.fetch, window, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	ids := matchIDs(matches)
	if !ids[1] {
		t.Error("kickoff exactly at window start must be included")
	}
	if ids[2] {
		t.Error("kickoff exactly at window end must be excluded")
	}
}

func TestCollectFinished_DedupsByID(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	window := timeutil.NewDayWindow(now, time.UTC, 3)
	calls := 0
	kickoff := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	fetch := func(_ context.Context, _ time.Time, _ []string) ([]api.Match, error) {
		calls++
		// Same two finished matches surface on every UTC date; dedup keeps each once.
		return []api.Match{
			finishedMatch(100, kickoff),
			finishedMatch(101, kickoff),
		}, nil
	}

	matches, failed, err := collectFinished(context.Background(), fetch, window, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(failed) != 0 {
		t.Errorf("failedDates = %v, want empty", failed)
	}
	if calls != 3 {
		t.Errorf("fetch called %d times, want 3", calls)
	}
	if len(matches) != 2 {
		t.Errorf("returned %d matches, want 2 after dedup", len(matches))
	}
}

func TestCollectFinished_DropsMatchesWithoutTimestamps(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	window := timeutil.NewDayWindow(now, time.UTC, 1)
	fetch := func(_ context.Context, _ time.Time, _ []string) ([]api.Match, error) {
		return []api.Match{
			{ID: 1, Status: api.MatchStatusFinished}, // no MatchTime
			finishedMatch(2, time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC)),
		}, nil
	}

	matches, _, err := collectFinished(context.Background(), fetch, window, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	ids := matchIDs(matches)
	if ids[1] {
		t.Error("timestamp-less match cannot be attributed and must be dropped")
	}
	if !ids[2] {
		t.Error("timestamped in-window match must survive")
	}
}

func TestCollectFinished_PartialFailureFlagsDegraded(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	window := timeutil.NewDayWindow(now, time.UTC, 3)

	f := newFakeDayFetcher()
	f.errFor["2026-06-11"] = errors.New("upstream blew up")
	f.payload["2026-06-10"] = []api.Match{finishedMatch(10, time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC))}
	f.payload["2026-06-12"] = []api.Match{finishedMatch(12, time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC))}

	matches, failed, err := collectFinished(context.Background(), f.fetch, window, false)
	if err != nil {
		t.Fatalf("partial-success should not return err, got %v", err)
	}
	if len(failed) != 1 || failed[0] != "2026-06-11" {
		t.Errorf("failedDates = %v, want [2026-06-11] (UTC query date)", failed)
	}
	if len(matches) != 2 {
		t.Errorf("matches len = %d, want 2", len(matches))
	}
}

func TestCollectFinished_AllFailureReturnsError(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	window := timeutil.NewDayWindow(now, time.UTC, 2)
	fetch := func(_ context.Context, _ time.Time, _ []string) ([]api.Match, error) {
		return nil, errors.New("nope")
	}

	matches, failed, err := collectFinished(context.Background(), fetch, window, false)
	if err == nil {
		t.Fatalf("expected err when all UTC dates fail")
	}
	if matches != nil {
		t.Errorf("matches not nil on total failure: %v", matches)
	}
	if len(failed) != 2 {
		t.Errorf("failedDates = %v, want 2", failed)
	}
}

func TestRunFinished_TimeoutNotSwallowed(t *testing.T) {
	t.Setenv(EnvOffline, "")
	t.Setenv(EnvAgent, "")

	// 1ns timeout — collectFinished may swallow per-day failures and return
	// no error. The CLI must still report timeout to the agent.
	var stdout, stderr bytes.Buffer
	code := runFinished(&stdout, &stderr, finishedFlags{cliFlags: cliFlags{mock: false, timeout: 1}, days: 1})
	if code != ExitTimeout {
		t.Errorf("exit = %d, want %d (stderr=%s)", code, ExitTimeout, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on timeout, got: %s", stdout.String())
	}
}

// TestCollectFinished_IncludeUpcomingTodayOnly verifies the upcoming gate uses
// the local today sub-window, not the fetched UTC date.
func TestCollectFinished_IncludeUpcomingTodayOnly(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	window := timeutil.NewDayWindow(now, time.UTC, 2) // [Jun11 00:00Z, Jun13 00:00Z)
	fetch := func(_ context.Context, date time.Time, _ []string) ([]api.Match, error) {
		day := date.UTC().Format("2006-01-02")
		switch day {
		case "2026-06-11":
			return []api.Match{
				finishedMatch(110, time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)),
				upcomingMatch(111, time.Date(2026, 6, 11, 13, 0, 0, 0, time.UTC)), // yesterday local -> gated out
			}, nil
		case "2026-06-12":
			return []api.Match{
				finishedMatch(120, time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)),
				upcomingMatch(121, time.Date(2026, 6, 12, 13, 0, 0, 0, time.UTC)), // today local -> kept when enabled
			}, nil
		}
		return nil, nil
	}

	matches, _, err := collectFinished(context.Background(), fetch, window, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	for _, m := range matches {
		if m.Status != api.MatchStatusFinished {
			t.Errorf("include=false leaked status=%q match=%d", m.Status, m.ID)
		}
	}
	if len(matches) != 2 {
		t.Errorf("include=false count = %d, want 2", len(matches))
	}

	matches, _, err = collectFinished(context.Background(), fetch, window, true)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	statusCounts := map[api.MatchStatus]int{}
	ids := map[int]bool{}
	for _, m := range matches {
		statusCounts[m.Status]++
		ids[m.ID] = true
	}
	if statusCounts[api.MatchStatusFinished] != 2 {
		t.Errorf("finished count = %d, want 2", statusCounts[api.MatchStatusFinished])
	}
	if statusCounts[api.MatchStatusNotStarted] != 1 || !ids[121] {
		t.Errorf("not_started = %v, want only match 121 (local today)", matches)
	}
}

func TestRunFinished_IncludeUpcomingFlag(t *testing.T) {
	t.Setenv(EnvOffline, "")
	t.Setenv(EnvAgent, "")

	// Mock data is finished-only; the flag should not break mock execution.
	var stdout, stderr bytes.Buffer
	code := runFinished(&stdout, &stderr, finishedFlags{
		cliFlags:        cliFlags{mock: true, timeout: time.Second},
		days:            1,
		includeUpcoming: true,
	})
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}
}

func TestResolveLocation_PrecedenceAndUTC(t *testing.T) {
	// Explicit IANA name wins.
	loc, err := resolveLocation("Asia/Tokyo")
	if err != nil {
		t.Fatalf("explicit tz: %v", err)
	}
	if loc.String() != "Asia/Tokyo" {
		t.Errorf("loc = %q, want Asia/Tokyo", loc.String())
	}

	// UTC is always available and reproduces pure-UTC semantics.
	loc, err = resolveLocation("UTC")
	if err != nil || loc != time.UTC {
		t.Errorf("UTC resolution = (%v, %v)", loc, err)
	}

	// Empty explicit falls back to the environment variable.
	t.Setenv(EnvTimezone, "America/New_York")
	loc, err = resolveLocation("")
	if err != nil {
		t.Fatalf("env tz: %v", err)
	}
	if loc.String() != "America/New_York" {
		t.Errorf("loc = %q, want America/New_York from %s", loc.String(), EnvTimezone)
	}

	// Invalid names surface an error so runFinished can map them to invalid_args.
	if _, err := resolveLocation("Not/AZone"); err == nil {
		t.Error("invalid timezone should return an error")
	}
}

func TestRunFinished_InvalidTimezoneIsInvalidArgs(t *testing.T) {
	t.Setenv(EnvOffline, "")
	t.Setenv(EnvAgent, "")

	var stdout, stderr bytes.Buffer
	code := runFinished(&stdout, &stderr, finishedFlags{
		cliFlags: cliFlags{mock: true, timeout: time.Second},
		days:     1,
		tz:       "Not/AZone",
	})
	if code != ExitInvalidArgs {
		t.Errorf("exit = %d, want %d (stderr=%s)", code, ExitInvalidArgs, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on invalid tz, got: %s", stdout.String())
	}
	var env errEnvelope
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal stderr: %v", err)
	}
	if env.Code != ErrCodeInvalidArgs {
		t.Errorf("code = %q, want %q", env.Code, ErrCodeInvalidArgs)
	}
}

// TestCollectFinished_DSTWindowTabs ensures a DST day in America/New_York
// still maps to two UTC dates and both get read.
func TestCollectFinished_DSTWindowTabs(t *testing.T) {
	ny := mustLoadLoc(t, "America/New_York")
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, ny) // spring-forward day
	window := timeutil.NewDayWindow(now, ny, 1)

	f := newFakeDayFetcher()
	_, _, err := collectFinished(context.Background(), f.fetch, window, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := []string{"2026-03-08", "2026-03-09"}
	if len(f.gotTabs) != len(want) {
		t.Fatalf("fetched %v, want UTC dates %v", f.gotTabs, want)
	}
	for _, d := range want {
		if _, ok := f.gotTabs[d]; !ok {
			t.Errorf("missing fetch for %s; got %v", d, f.gotTabs)
		}
	}
}

package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/0xjuanma/golazo/internal/api"
	"github.com/0xjuanma/golazo/internal/data"
	"github.com/0xjuanma/golazo/internal/fotmob"
	"github.com/0xjuanma/golazo/internal/timeutil"
	"github.com/spf13/cobra"
)

// MaxFinishedDays is the maximum supported lookback window for `golazo finished`.
const MaxFinishedDays = 7

// EnvTimezone lets agents pin the day-window timezone without passing --tz on
// every invocation. The explicit flag always wins over this variable.
const EnvTimezone = "GOLAZO_TZ"

// finishedDayFetcher abstracts MatchesByDateWithTabs for testing.
type finishedDayFetcher func(ctx context.Context, date time.Time, tabs []string) ([]api.Match, error)

// resolveLocation resolves the timezone used to interpret natural days.
//
// Precedence: explicit --tz value, then the GOLAZO_TZ environment variable,
// then the process-local timezone. Values are IANA names (e.g.
// "America/New_York", "Asia/Tokyo"); "UTC" is always accepted and reproduces
// pure-UTC calendar-day semantics for callers that need the legacy behavior.
func resolveLocation(explicit string) (*time.Location, error) {
	name := strings.TrimSpace(explicit)
	if name == "" {
		name = strings.TrimSpace(os.Getenv(EnvTimezone))
	}
	if name == "" {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: %w", name, err)
	}
	return loc, nil
}

// collectFinished executes the fetch plan derived from window.
//
// The window is the single source of truth for day attribution:
//   - window.UTCDates decides which UTC calendar days are read (an N-day
//     local window maps to N, N+1 or N+2 UTC dates);
//   - UTC dates intersecting the local today sub-window get fixtures+results,
//     earlier UTC dates get results only;
//   - every returned match is attributed by the same half-open local interval
//     and deduplicated by ID, so midnight fixtures follow the user's local day.
//
// It returns the union of in-window finished matches (plus today's
// not-started matches when includeUpcoming is true), the UTC date strings
// whose fetch failed, and an error iff ALL UTC dates failed.
func collectFinished(ctx context.Context, fetch finishedDayFetcher, window timeutil.DayWindow, includeUpcoming bool) ([]api.Match, []string, error) {
	utcDates := window.UTCDates()
	today := window.Today()

	dedup := make(map[int]api.Match, len(utcDates)*10)
	var failedDates []string
	successCount := 0
	var lastErr error

	for _, date := range utcDates {
		// failed_dates deliberately stay UTC YYYY-MM-DD: they identify the
		// concrete upstream query that failed, matching the client cache key.
		dateStr := date.Format("2006-01-02")

		tabs := []string{"results"}
		if today.IntersectsUTC(date) {
			tabs = []string{"fixtures", "results"}
		}

		matches, err := fetch(ctx, date, tabs)
		if err != nil {
			failedDates = append(failedDates, dateStr)
			lastErr = err
			continue
		}
		successCount++

		for _, m := range matches {
			if m.MatchTime == nil {
				// No kickoff instant means no deterministic local-day attribution.
				continue
			}
			switch m.Status {
			case api.MatchStatusFinished:
				if !window.Contains(*m.MatchTime) {
					continue
				}
			case api.MatchStatusNotStarted:
				if !includeUpcoming || !today.Contains(*m.MatchTime) {
					continue
				}
			default:
				continue
			}
			if _, exists := dedup[m.ID]; !exists {
				dedup[m.ID] = m
			}
		}
	}

	if successCount == 0 {
		return nil, failedDates, lastErr
	}

	out := make([]api.Match, 0, len(dedup))
	for _, m := range dedup {
		out = append(out, m)
	}
	return out, failedDates, nil
}

func defaultFinishedFetcher(c *fotmob.Client) finishedDayFetcher {
	return c.MatchesByDateWithTabs
}

// finishedFlags extends the common flag set with --days, --include-upcoming and --tz.
type finishedFlags struct {
	cliFlags
	days            int
	includeUpcoming bool
	tz              string
}

var finishedFlagSet finishedFlags

// runFinished is the testable core of the `finished` subcommand.
func runFinished(stdout, stderr io.Writer, flags finishedFlags) int {
	applyPretty(flags.cliFlags)

	if flags.days < 1 || flags.days > MaxFinishedDays {
		return WriteError(stderr, ErrCodeInvalidArgs,
			NewInvalidArg("--days must be between 1 and %d, got %d", MaxFinishedDays, flags.days))
	}

	// Resolve the timezone before any network work so an invalid value fails
	// fast as invalid_args instead of an upstream error.
	loc, err := resolveLocation(flags.tz)
	if err != nil {
		return WriteError(stderr, ErrCodeInvalidArgs, NewInvalidArg("%s", err.Error()))
	}

	client, ctx, cancel, err := newHeadlessClient(runtimeOpts{
		mock:    flags.mock,
		debug:   flags.debug,
		timeout: flags.timeout,
	})
	defer cancel()
	if err == ErrOffline {
		return WriteError(stderr, ErrCodeOffline, err)
	}
	if err != nil {
		return WriteError(stderr, ErrCodeUpstreamError, err)
	}

	var (
		matches     []api.Match
		failedDates []string
	)

	if flags.mock {
		// Mock data is single-day; serve it regardless of --days.
		matches = data.MockFinishedMatches()
	} else {
		// Capture the clock once and derive the full UTC fetch plan from the
		// local window. No per-day time.Now calls means the window cannot drift
		// even if the multi-day load straddles midnight.
		window := timeutil.NewDayWindow(time.Now(), loc, flags.days)
		matches, failedDates, err = collectFinished(ctx, defaultFinishedFetcher(client), window, flags.includeUpcoming)
		if err != nil {
			return WriteError(stderr, ClassifyClientError(err, isTimeout(ctx)), err)
		}
		// Guard against silent timeouts: per-day fetches may all swallow the
		// cancellation and return empty without an error. Surface as timeout
		// so agents can distinguish "no finished matches" from "timed out".
		if isTimeout(ctx) {
			return WriteError(stderr, ErrCodeTimeout,
				fmt.Errorf("finished matches fetch timed out after %s", flags.timeout))
		}
	}

	SortMatches(matches)

	var writeErr error
	if len(failedDates) > 0 {
		writeErr = WriteDegraded(stdout, matches, failedDates)
	} else {
		writeErr = WriteJSON(stdout, matches)
	}
	if writeErr != nil {
		return WriteError(stderr, ErrCodeUpstreamError, writeErr)
	}
	return ExitOK
}

var finishedCmd = &cobra.Command{
	Use:   "finished",
	Short: "List finished matches over a local day window as JSON",
	Long: `Fetches finished matches for the last --days local calendar days (default 1 = today) across active leagues. Days are interpreted in the selected timezone (see --tz); the required UTC dates are derived from those local days, so matches near midnight stay attributed to your local day. Use --include-upcoming to also include today's not-yet-started matches. Partial failures surface as degraded:true with failed_dates listed as UTC YYYY-MM-DD query dates.

Timezone resolution: --tz flag, then the GOLAZO_TZ environment variable, then the system local timezone. Pass --tz UTC for pure-UTC calendar-day semantics.

Example output:
  {"status":"ok","count":2,"data":[{"id":4506420,"league":{"id":47,"name":"Premier League","country":"England"},"home_team":{"name":"Liverpool","short_name":"Liverpool"},"away_team":{"name":"Arsenal","short_name":"Arsenal"},"status":"finished","home_score":3,"away_score":1,"match_time":"2026-06-12T15:00:00Z"}]}

Degraded example (one UTC date failed but others succeeded):
  {"status":"ok","degraded":true,"failed_dates":["2026-06-10"],"count":12,"data":[...]}`,
	SilenceUsage:  true,
	SilenceErrors: true,
	Run: func(cmd *cobra.Command, args []string) {
		code := runFinished(os.Stdout, os.Stderr, finishedFlagSet)
		if code != ExitOK {
			os.Exit(code)
		}
	},
}

func init() {
	addCommonCLIFlags(finishedCmd, &finishedFlagSet.cliFlags)
	finishedCmd.Flags().IntVar(&finishedFlagSet.days, "days", 1, "Number of local calendar days to look back (1..7)")
	finishedCmd.Flags().BoolVar(&finishedFlagSet.includeUpcoming, "include-upcoming", false, "Also include today's not-yet-started matches in the result")
	finishedCmd.Flags().StringVar(&finishedFlagSet.tz, "tz", "", "IANA timezone for local day boundaries (e.g. America/New_York, Asia/Tokyo, UTC); defaults to "+EnvTimezone+" then the system zone")
	rootCmd.AddCommand(finishedCmd)
}

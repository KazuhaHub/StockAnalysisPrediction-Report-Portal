package app

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// errForecastPurgeHistoryUnavailable marks a scoped marker lookup that could not be answered, so the
// read is refused rather than answered from the global history.
var errForecastPurgeHistoryUnavailable = errors.New("purge history unavailable")

// forecastPurgeView is what one read knows about retention. The two maps differ only for an own
// population, and they are not interchangeable.
//
// filter is the site-wide history. It removes sets from every computation, so no partial data from a
// month retention has emptied is ever shown, and it keeps cache identity honest across processes.
//
// disclosed is the subset this population may be told about: the months its current subtree owned
// sets in when they were purged. It alone drives the range extension, the exclusion lists, the
// purged groups and cells, and the drawer's purged state. For a nil population (all readers, the
// administrator aliases, external-all readers) it is the same map as filter, which keeps their reads
// exactly as they were.
type forecastPurgeView struct {
	filter, disclosed map[[2]string]string
}

// loadForecastPurgeView fails closed. A population that needs ownership evidence never falls back to
// the global history when that evidence cannot be read; a nil population never touches it.
func (s *Server) loadForecastPurgeView(pop forecastPopulation) (forecastPurgeView, error) {
	filter, err := s.st.loadPredictionPurgeMarkers()
	if err != nil {
		return forecastPurgeView{}, err
	}
	if pop.OUs == nil {
		return forecastPurgeView{filter: filter, disclosed: filter}, nil
	}
	disclosed, err := s.st.loadPredictionPurgeMarkersForPopulation(pop)
	if err != nil {
		logForecastPurgeHistoryUnavailable(err)
		return forecastPurgeView{}, fmt.Errorf("%w: %v", errForecastPurgeHistoryUnavailable, err)
	}
	return forecastPurgeView{filter: filter, disclosed: disclosed}, nil
}

// forecastPurgeHistoryLoggedAt is the unix second of the last log line. Until the evidence table
// exists, every own read fails the same way, and one line a minute says so without flooding the log.
var forecastPurgeHistoryLoggedAt atomic.Int64

func logForecastPurgeHistoryUnavailable(err error) {
	now := time.Now().Unix()
	last := forecastPurgeHistoryLoggedAt.Load()
	if now-last < 60 || !forecastPurgeHistoryLoggedAt.CompareAndSwap(last, now) {
		return
	}
	log.Printf("forecast purge history for an own population is unavailable, so its reads are refused: %v", err)
}

func writeForecastPurgeViewError(w http.ResponseWriter, err error) {
	if errors.Is(err, errForecastPurgeHistoryUnavailable) {
		jsonErrorCode(w, http.StatusServiceUnavailable, "forecast_purge_history_unavailable", "purge history is unavailable")
		return
	}
	jsonErrorCode(w, http.StatusInternalServerError, "db_error", "database error")
}

func forecastPurgeCacheTag(months []forecastPurgedMonth) string {
	if len(months) == 0 {
		return ""
	}
	var b strings.Builder
	for _, month := range months {
		b.WriteString(month.Cohort)
		b.WriteByte(0)
		b.WriteString(month.PurgedAt)
		b.WriteByte(0)
	}
	return "\x00purges:" + sha256Hex([]byte(b.String()))
}

// Include retention-only months even when no surviving set can establish the monthly range.
func forecastEarliestWithPurges(earliest, rtype string, purges map[[2]string]string) string {
	for key := range purges {
		day := key[1] + "-01"
		if key[0] == rtype && (earliest == "" || day < earliest) {
			earliest = day
		}
	}
	return earliest
}

func forecastExcludedMonths(rtype, from, to string, purges map[[2]string]string) []forecastPurgedMonth {
	var out []forecastPurgedMonth
	if from == "" || to == "" {
		return nil
	}
	for key, at := range purges {
		if key[0] == rtype && key[1] >= from[:7] && key[1] <= to[:7] {
			out = append(out, forecastPurgedMonth{Cohort: key[1], PurgedAt: at})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cohort < out[j].Cohort })
	return out
}

func forecastWithoutPurgedSets(sets []*groupSet, rtype string, purges map[[2]string]string) []*groupSet {
	out := make([]*groupSet, 0, len(sets))
	for _, set := range sets {
		if _, purged := purges[[2]string{rtype, set.Cohort}]; !purged {
			out = append(out, set)
		}
	}
	return out
}

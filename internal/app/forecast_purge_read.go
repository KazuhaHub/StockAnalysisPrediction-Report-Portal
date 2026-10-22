package app

import (
	"sort"
	"strings"
)

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

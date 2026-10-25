package app

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type reportOverviewCount struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}
type reportOverview struct {
	Yesterday   int64                            `json:"yesterday"`
	Month       int64                            `json:"month"`
	Year        int64                            `json:"year"`
	Total       int64                            `json:"total"`
	Today       int64                            `json:"today"`
	Recent      int64                            `json:"recent"`
	Days        []reportOverviewCount            `json:"days"`
	Kinds       []reportOverviewCount            `json:"kinds"`
	KindPeriods map[string][]reportOverviewCount `json:"kind_periods"`
}

// Report dates are civil dates; ingestion time and internal pipeline artifacts are not report output.
func (s *Store) reportOverview(today string) (reportOverview, error) {
	return s.reportOverviewWindow(today, 7)
}
func (s *Store) reportOverviewWindow(today string, window int) (reportOverview, error) {
	out := reportOverview{Days: []reportOverviewCount{}, Kinds: []reportOverviewCount{}}
	end, err := time.Parse("2006-01-02", today)
	if err != nil {
		return out, err
	}
	days := map[string]int{}
	for i := 0; i < window; i++ {
		key := end.AddDate(0, 0, i-window+1).Format("2006-01-02")
		days[key] = i
		out.Days = append(out.Days, reportOverviewCount{Key: key})
	}
	args := []any{}
	placeholders := []string{}
	for typ := range internalTypes {
		args = append(args, typ)
		placeholders = append(placeholders, "?")
	}
	where := ""
	if len(placeholders) > 0 {
		where = " WHERE COALESCE(rtype, '') NOT IN (" + strings.Join(placeholders, ",") + ")"
	}
	rows, err := s.query("SELECT COALESCE(kind, ''), COALESCE(rdate, ''), COUNT(*) FROM reports"+where+" GROUP BY COALESCE(kind, ''), COALESCE(rdate, '')", args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	kinds := map[string]int64{}
	periods := map[string]map[string]int64{}
	for _, span := range []int{7, 30, 90, 365} {
		periods[strconv.Itoa(span)] = map[string]int64{}
	}
	out.KindPeriods = map[string][]reportOverviewCount{}
	for rows.Next() {
		var kind, date string
		var count int64
		if err := rows.Scan(&kind, &date, &count); err != nil {
			return out, err
		}
		out.Total += count
		kinds[kind] += count
		if len(date) == 10 && date <= today {
			for _, span := range []int{7, 30, 90, 365} {
				if date >= end.AddDate(0, 0, 1-span).Format("2006-01-02") {
					periods[strconv.Itoa(span)][kind] += count
				}
			}
		}
		if index, ok := days[date]; ok {
			out.Days[index].Count += count
		}
		if date >= end.AddDate(0, 0, -6).Format("2006-01-02") && date <= today {
			out.Recent += count
		}
		if date == end.AddDate(0, 0, -1).Format("2006-01-02") {
			out.Yesterday += count
		}
		if len(date) == 10 && date <= today {
			if date >= end.AddDate(0, 0, -29).Format("2006-01-02") {
				out.Month += count
			}
			if date >= end.AddDate(0, 0, -364).Format("2006-01-02") {
				out.Year += count
			}
		}
		if date == today {
			out.Today += count
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	for key, count := range kinds {
		out.Kinds = append(out.Kinds, reportOverviewCount{Key: key, Count: count})
	}
	sort.Slice(out.Kinds, func(i, j int) bool {
		if out.Kinds[i].Count == out.Kinds[j].Count {
			return out.Kinds[i].Key < out.Kinds[j].Key
		}
		return out.Kinds[i].Count > out.Kinds[j].Count
	})
	for span, values := range periods {
		items := []reportOverviewCount{}
		for key, count := range values {
			items = append(items, reportOverviewCount{Key: key, Count: count})
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].Count == items[j].Count {
				return items[i].Key < items[j].Key
			}
			return items[i].Count > items[j].Count
		})
		out.KindPeriods[span] = items
	}
	return out, nil
}

func (s *Server) apiReportOverview(w http.ResponseWriter, r *http.Request, user string) {
	out, err := s.st.reportOverviewWindow(time.Now().In(s.panelLocation()).Format("2006-01-02"), 365)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "report overview unavailable")
		return
	}
	writeJSON(w, out)
}

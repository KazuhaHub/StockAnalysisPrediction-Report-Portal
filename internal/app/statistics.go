package app

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type statisticsQuery struct {
	dataset, group, from, to, search, interval string
	page, size                                 int
}
type statisticsRow struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

// SQL identifiers are selected exclusively from this registry, never request strings.
var statisticsDimensions = map[string]map[string]string{
	"reports":  {"kind": "kind", "subtype": "rtype", "symbol": "symbol", "date": "rdate"},
	"runs":     {"status": "status", "target": "COALESCE((SELECT name FROM batch_targets WHERE id=batch_jobs.target_id), CAST(target_id AS TEXT))", "submitter": "created_by"},
	"accounts": {"role": "role", "organization": "COALESCE((SELECT name FROM user_groups WHERE id=users.group_id), CAST(group_id AS TEXT))", "source": "source", "active": "CAST(active AS TEXT)"},
}

func parseStatisticsQuery(r *http.Request) (statisticsQuery, error) {
	v := r.URL.Query()
	q := statisticsQuery{dataset: v.Get("dataset"), group: v.Get("group"), from: v.Get("from"), to: v.Get("to"), search: strings.TrimSpace(v.Get("q")), page: 1, size: 25}
	if q.dataset == "" {
		q.dataset = "reports"
	}
	if q.group == "" {
		q.group = map[string]string{"reports": "kind", "runs": "status", "accounts": "role"}[q.dataset]
	}
	if statisticsDimensions[q.dataset][q.group] == "" {
		return q, fmt.Errorf("invalid dataset or dimension")
	}
	for _, d := range []string{q.from, q.to} {
		if d != "" {
			if _, err := time.Parse("2006-01-02", d); err != nil {
				return q, fmt.Errorf("invalid date")
			}
		}
	}
	if q.from != "" && q.to != "" && q.from > q.to {
		return q, fmt.Errorf("invalid date range")
	}
	if len(q.search) > 200 {
		return q, fmt.Errorf("search is too long")
	}
	for key, dest := range map[string]*int{"page": &q.page, "page_size": &q.size} {
		if v.Get(key) != "" {
			n, err := strconv.Atoi(v.Get(key))
			if err != nil || n < 1 || n > 1000000 {
				return q, fmt.Errorf("invalid pagination")
			}
			*dest = n
		}
	}
	if q.size > 100 {
		q.size = 100
	}
	q.interval = v.Get("interval")
	if q.interval != "" && q.interval != "day" && q.interval != "month" && q.interval != "year" {
		return q, fmt.Errorf("invalid interval")
	}
	if v.Get("view") != "" && v.Get("view") != "chart" {
		return q, fmt.Errorf("invalid view")
	}
	if f := v.Get("format"); f != "" && f != "csv" {
		return q, fmt.Errorf("invalid format")
	}
	return q, nil
}
func (q statisticsQuery) sql(loc *time.Location) (string, []any) {
	table := map[string]string{"reports": "reports", "runs": "batch_jobs", "accounts": "users"}[q.dataset]
	dimension := "COALESCE(" + statisticsDimensions[q.dataset][q.group] + ", '')"
	if q.group == "date" {
		if q.interval == "month" {
			dimension = "SUBSTR(" + dimension + ", 1, 7)"
		}
		if q.interval == "year" {
			dimension = "SUBSTR(" + dimension + ", 1, 4)"
		}
	}
	predicates := []string{"1=1"}
	args := []any{}
	if q.dataset == "reports" {
		for typ := range internalTypes {
			predicates = append(predicates, "COALESCE(rtype, '') <> ?")
			args = append(args, typ)
		}
	}
	dateCol := "created_at"
	if q.dataset == "reports" {
		dateCol = "rdate"
	}
	for _, edge := range []struct {
		date, op string
		next     bool
	}{{q.from, ">=", false}, {q.to, "<", true}} {
		if edge.date == "" {
			continue
		}
		d, _ := time.ParseInLocation("2006-01-02", edge.date, loc)
		if edge.next {
			d = d.AddDate(0, 0, 1)
		}
		value := d.In(time.Local).Format("2006-01-02 15:04:05")
		if q.dataset == "reports" {
			value = d.Format("2006-01-02")
		}
		predicates = append(predicates, dateCol+" "+edge.op+" ?")
		args = append(args, value)
	}
	if q.search != "" {
		predicates = append(predicates, "LOWER("+dimension+") LIKE ? ESCAPE '!'")
		term := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(strings.ToLower(q.search))
		args = append(args, "%"+term+"%")
	}
	return "SELECT " + dimension + " AS label, COUNT(*) AS count FROM " + table + " WHERE " + strings.Join(predicates, " AND ") + " GROUP BY " + dimension, args
}
func (s *Server) apiStatistics(w http.ResponseWriter, r *http.Request, user string) {
	q, err := parseStatisticsQuery(r)
	if err != nil {
		jsonErrorCode(w, http.StatusBadRequest, "statistics_query_invalid", "invalid statistics query")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	base, args := q.sql(s.panelLocation())
	ordered := base + " ORDER BY count DESC, label ASC"
	if r.URL.Query().Get("format") == "csv" {
		rows, err := s.st.db.QueryContext(ctx, s.st.bind(ordered), args...)
		if err != nil {
			jsonErrorCode(w, http.StatusInternalServerError, "statistics_unavailable", "statistics unavailable")
			return
		}
		defer rows.Close()
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="statistics-`+q.dataset+`-`+q.group+`.csv"`)
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, "\ufeff")
		out := csv.NewWriter(w)
		out.Write([]string{q.group, "count"})
		for rows.Next() {
			var row statisticsRow
			if err := rows.Scan(&row.Label, &row.Count); err != nil {
				panic(http.ErrAbortHandler)
			}
			label := row.Label
			if strings.HasPrefix(strings.TrimSpace(label), "=") || strings.ContainsAny(firstStatisticsCharacter(label), "+-@\t\r\n") {
				label = "'" + label
			}
			if err := out.Write([]string{label, strconv.FormatInt(row.Count, 10)}); err != nil {
				return
			}
		}
		out.Flush()
		if rows.Err() != nil {
			panic(http.ErrAbortHandler)
		}
		return
	}
	var total, records int64
	if err := s.st.db.QueryRowContext(ctx, s.st.bind("SELECT COUNT(*), COALESCE(SUM(count),0) FROM ("+base+") statistics_groups"), args...).Scan(&total, &records); err != nil {
		jsonErrorCode(w, http.StatusInternalServerError, "statistics_unavailable", "statistics unavailable")
		return
	}
	chart := r.URL.Query().Get("view") == "chart"
	tooMany := chart && q.group == "date" && total > 400
	limit, offset := q.size, (q.page-1)*q.size
	if chart {
		limit, offset = 20, 0
		if q.group == "date" {
			limit = 400
			ordered = base + " ORDER BY label ASC"
		}
	}
	pageArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.st.db.QueryContext(ctx, s.st.bind(ordered+" LIMIT ? OFFSET ?"), pageArgs...)
	if err != nil {
		jsonErrorCode(w, http.StatusInternalServerError, "statistics_unavailable", "statistics unavailable")
		return
	}
	defer rows.Close()
	items := []statisticsRow{}
	for rows.Next() {
		var row statisticsRow
		if err := rows.Scan(&row.Label, &row.Count); err != nil {
			jsonErrorCode(w, http.StatusInternalServerError, "statistics_unavailable", "statistics unavailable")
			return
		}
		items = append(items, row)
	}
	if rows.Err() != nil {
		jsonErrorCode(w, http.StatusInternalServerError, "statistics_unavailable", "statistics unavailable")
		return
	}
	var other int64
	if chart && q.group != "date" {
		other = records
		for _, item := range items {
			other -= item.Count
		}
	}
	if tooMany {
		items = []statisticsRow{}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"items": items, "other": other, "too_many_points": tooMany, "total": total, "records": records, "page": q.page, "page_size": q.size, "today": time.Now().In(s.panelLocation()).Format("2006-01-02")})
}
func firstStatisticsCharacter(value string) string {
	value = strings.TrimLeft(value, " ")
	if value == "" {
		return ""
	}
	return value[:1]
}

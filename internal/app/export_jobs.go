package app

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

// Export task persistence and file layout (ADR 0045).

const (
	exportQueued   = "queued"
	exportRunning  = "running"
	exportDone     = "done"
	exportFailed   = "failed"
	exportCanceled = "canceled"
	exportExpired  = "expired"

	exportFileTTL    = 7 * 24 * time.Hour  // a finished file is kept this long
	exportRowTTL     = 90 * 24 * time.Hour // a job row is kept this long
	exportErrorLimit = 500
)

// exportOptions are the report-archive options. The zero value is a plain flat folder of Markdown.
type exportOptions struct {
	FrontMatter bool   `json:"front_matter"`
	IndexCSV    bool   `json:"index_csv"`
	Layout      string `json:"layout"` // flat | date | symbol
}

// exportParams is the JSON stored in export_jobs.params: everything the worker needs to reproduce
// the preview the administrator saw.
type exportParams struct {
	Name    string        `json:"name"`
	Filter  exportNode    `json:"filter"`
	Columns []string      `json:"columns"`
	TZ      string        `json:"tz"`
	Options exportOptions `json:"options"`
	// Reader marks a job submitted through the reader endpoints (ADR 0046). Cap is the number of
	// reports it was charged for: the worker stops there however many reports match by then.
	// MaxID is the highest reports.id at submit: reports ingested later are never part of the job, so
	// they cannot displace the ones that were counted (the scan runs newest first).
	Reader bool  `json:"reader,omitempty"`
	Cap    int64 `json:"cap,omitempty"`
	MaxID  int64 `json:"max_id,omitempty"`
}

// exportJob is the stored row. Params stays raw until a caller needs it.
type exportJob struct {
	ID, Source, Format, Params, RequestedBy, Status string
	RowsEstimate, RowsDone, Bytes                   int64
	FileName, Error                                 string
	CreatedAt, StartedAt, FinishedAt, ExpiresAt     int64
}

// isReader reports whether the job was submitted through the reader endpoints (ADR 0046).
func (j *exportJob) isReader() bool {
	var p exportParams
	return json.Unmarshal([]byte(j.Params), &p) == nil && p.Reader
}

// exportJobJSON is the wire shape of a job (the API contract in ADR 0045).
type exportJobJSON struct {
	ID           string     `json:"id"`
	Source       string     `json:"source"`
	Format       string     `json:"format"`
	Name         string     `json:"name"`
	RequestedBy  string     `json:"requested_by"`
	Status       string     `json:"status"`
	RowsEstimate int64      `json:"rows_estimate"`
	RowsDone     int64      `json:"rows_done"`
	Bytes        int64      `json:"bytes"`
	FileName     string     `json:"file_name"`
	Error        string     `json:"error"`
	CreatedAt    int64      `json:"created_at"`
	StartedAt    int64      `json:"started_at"`
	FinishedAt   int64      `json:"finished_at"`
	ExpiresAt    int64      `json:"expires_at"`
	Filter       exportNode `json:"filter"`
	Columns      []string   `json:"columns"`
}

func (j *exportJob) params() exportParams {
	var p exportParams
	_ = json.Unmarshal([]byte(j.Params), &p)
	p.Filter = normalizeExportFilter(p.Filter)
	return p
}

func (j *exportJob) view() exportJobJSON {
	p := j.params()
	cols := p.Columns
	if cols == nil {
		cols = []string{}
	}
	return exportJobJSON{
		ID: j.ID, Source: j.Source, Format: j.Format, Name: p.Name, RequestedBy: j.RequestedBy, Status: j.Status,
		RowsEstimate: j.RowsEstimate, RowsDone: j.RowsDone, Bytes: j.Bytes, FileName: j.FileName, Error: j.Error,
		CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt, ExpiresAt: j.ExpiresAt,
		Filter: p.Filter, Columns: cols,
	}
}

const exportJobCols = `id,source,format,params,requested_by,status,rows_estimate,rows_done,bytes,file_name,error,
	created_at,started_at,finished_at,expires_at`

func scanExportJob(scan func(...any) error) (*exportJob, error) {
	var j exportJob
	if err := scan(&j.ID, &j.Source, &j.Format, &j.Params, &j.RequestedBy, &j.Status, &j.RowsEstimate, &j.RowsDone,
		&j.Bytes, &j.FileName, &j.Error, &j.CreatedAt, &j.StartedAt, &j.FinishedAt, &j.ExpiresAt); err != nil {
		return nil, err
	}
	return &j, nil
}

func newExportID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (s *Store) insertExportJob(j *exportJob) error {
	_, err := s.insertExportJobOn(s.db, j)
	return err
}

func (s *Store) insertExportJobOn(ex execer, j *exportJob) (sql.Result, error) {
	return ex.Exec(s.bind(`INSERT INTO export_jobs(`+exportJobCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		j.ID, j.Source, j.Format, j.Params, j.RequestedBy, j.Status, j.RowsEstimate, j.RowsDone, j.Bytes,
		j.FileName, j.Error, j.CreatedAt, j.StartedAt, j.FinishedAt, j.ExpiresAt)
}

// getExportJob returns nil for an unknown id.
func (s *Store) getExportJob(id string) *exportJob {
	j, err := scanExportJob(s.queryRow(`SELECT `+exportJobCols+` FROM export_jobs WHERE id=?`, id).Scan)
	if err != nil {
		return nil
	}
	return j
}

// listExportJobs lists newest first; a non-empty owner restricts it to that requester's jobs.
func (s *Store) listExportJobs(limit int, activeOnly bool, owner string) ([]*exportJob, error) {
	q := `SELECT ` + exportJobCols + ` FROM export_jobs`
	var where []string
	var args []any
	if activeOnly {
		where = append(where, `status IN ('queued','running')`)
	}
	if owner != "" {
		where = append(where, `requested_by=?`)
		args = append(args, owner)
	}
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ` + itoa64(int64(limit))
	rows, err := s.query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*exportJob{}
	for rows.Next() {
		j, err := scanExportJob(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// claimNextExportJob moves the oldest queued job to running. The conditional UPDATE is the claim, so a
// job canceled between the SELECT and the UPDATE is simply skipped.
func (s *Store) claimNextExportJob(now int64) (*exportJob, error) {
	for {
		var id string
		err := s.queryRow(`SELECT id FROM export_jobs WHERE status='queued' ORDER BY created_at, id LIMIT 1`).Scan(&id)
		if err == sql.ErrNoRows {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		res, err := s.exec(`UPDATE export_jobs SET status='running',started_at=?,rows_done=0,bytes=0 WHERE id=? AND status='queued'`, now, id)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return s.getExportJob(id), nil
		}
	}
}

// listOwnExportTasks backs the my-tasks tray (ADR 0047): every queued or running job of one requester
// plus their most recently finished ones (ranked by finish time), bounded by termLimit. A requester who is not an administrator
// only ever owns jobs submitted through the reader endpoints, so for them the listing is narrowed to
// those (the SQL prefilter is a cheap superset; isReader is the exact test, as in serveExportList).
func (s *Store) listOwnExportTasks(user string, readerOnly bool, termLimit int) ([]*exportJob, error) {
	extra := ""
	if readerOnly {
		extra = ` AND params LIKE '%"reader":true%'`
	}
	var out []*exportJob
	for _, q := range []string{
		`status IN ('queued','running')` + extra + ` ORDER BY created_at DESC, id DESC`,
		`status NOT IN ('queued','running')` + extra + ` ORDER BY CASE WHEN finished_at>0 THEN finished_at ELSE created_at END DESC, id DESC LIMIT ` + itoa64(int64(termLimit)),
	} {
		rows, err := s.query(`SELECT `+exportJobCols+` FROM export_jobs WHERE requested_by=? AND `+q, user)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			j, err := scanExportJob(rows.Scan)
			if err != nil {
				rows.Close()
				return nil, err
			}
			if readerOnly && !j.isReader() {
				continue
			}
			out = append(out, j)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// exportJobsAhead is how many jobs the worker will take before this queued one: the queued jobs it
// claims first (oldest created_at, then id — claimNextExportJob's order) plus the job running now.
// There is one export worker (exportLoop), so the running count is 0 or 1.
func (s *Store) exportJobsAhead(j *exportJob) (int, error) {
	var n int
	err := s.queryRow(`SELECT COUNT(*) FROM export_jobs WHERE
		(status='queued' AND (created_at<? OR (created_at=? AND id<?))) OR status='running'`,
		j.CreatedAt, j.CreatedAt, j.ID).Scan(&n)
	return n, err
}

func clampError(msg string) string {
	if len(msg) > exportErrorLimit {
		return truncateUTF8Bytes(msg, exportErrorLimit)
	}
	return msg
}

// ---------- files ----------

func exportExt(format string) string {
	switch format {
	case "jsonl":
		return ".jsonl"
	case "md_zip":
		return ".zip"
	}
	return ".csv"
}

// exportDir is <dataDir>/exports, next to the database like the other data-dir artefacts.
func (s *Server) exportDir() string {
	base := "data"
	if s.cfg != nil && s.cfg.DBPath != "" {
		base = config.DirOf(s.cfg.DBPath)
	}
	return filepath.Join(base, "exports")
}

// exportPath is the derived location of a job's finished file; it is never stored.
func (s *Server) exportPath(j *exportJob) string {
	return filepath.Join(s.exportDir(), j.ID+exportExt(j.Format))
}

func (s *Server) exportPartPath(id string) string { return filepath.Join(s.exportDir(), id+".part") }

// exportDirBytes is the size of the exports directory for the storage usage page.
func (s *Server) exportDirBytes() int64 {
	entries, err := os.ReadDir(s.exportDir())
	if err != nil {
		return 0
	}
	var n int64
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !e.IsDir() {
			n += info.Size()
		}
	}
	return n
}

// exportFileName builds the download name: the administrator's name (sanitized), or the source and
// the panel-zone date, plus the format's extension.
func (s *Server) exportFileName(source, format, name string) (base, file string) {
	ext := exportExt(format)
	name = strings.TrimSpace(name)
	if name == "" {
		name = source + "-" + time.Now().In(s.panelLocation()).Format("20060102")
	}
	name = strings.TrimSuffix(sanitizeFilename(name), ext)
	name = truncateUTF8Bytes(name, 120)
	if name == "" {
		name = "export"
	}
	return name, name + ext
}

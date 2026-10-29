package app

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The export worker (ADR 0045): one job at a time, streamed from the database in keyset batches into
// a ".part" file that is renamed on success, so a crash never leaves a file that looks complete.

// exportLoop is the long-lived goroutine: it drains the queue, then waits for a submission, a poll
// tick or the hourly sweep.
func (s *Server) exportLoop() {
	poll := time.NewTicker(5 * time.Second)
	sweep := time.NewTicker(time.Hour)
	defer poll.Stop()
	defer sweep.Stop()
	s.exportSweep(s.exports.clock())
	wake := s.exports.wakeCh()
	for {
		for s.exportRunNext() {
		}
		select {
		case <-wake:
		case <-poll.C:
		case <-sweep.C:
			s.exportSweep(s.exports.clock())
		}
	}
}

// exportRecover runs once at start, before the worker. A job that was running when the process died
// returns to the queue and its partial file is removed; nothing about a half-written file is trusted.
func (s *Server) exportRecover() {
	if _, err := s.st.exec(`UPDATE export_jobs SET status='queued',started_at=0,rows_done=0,bytes=0 WHERE status='running'`); err != nil {
		log.Printf("export: requeue interrupted jobs: %v", err)
	}
	entries, err := os.ReadDir(s.exportDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".part") || strings.HasSuffix(e.Name(), ".idx") {
			_ = os.Remove(filepath.Join(s.exportDir(), e.Name()))
		}
	}
}

// exportRunNext claims and runs one queued job. It reports whether it found one.
func (s *Server) exportRunNext() bool {
	j, err := s.st.claimNextExportJob(s.exports.clock().UnixMilli())
	if err != nil {
		log.Printf("export: claim: %v", err)
		return false
	}
	if j == nil {
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.exports.mu.Lock()
	if s.exports.cancels == nil {
		s.exports.cancels = map[string]context.CancelFunc{}
	}
	s.exports.cancels[j.ID] = cancel
	s.exports.mu.Unlock()
	defer func() {
		cancel()
		s.exports.mu.Lock()
		delete(s.exports.cancels, j.ID)
		s.exports.mu.Unlock()
	}()

	rows, bytesOut, err := s.exportExecuteSafe(ctx, j)
	now := s.exports.clock().UnixMilli()
	switch {
	case ctx.Err() != nil:
		s.exportRemoveWork(j.ID)
		s.st.exec(`UPDATE export_jobs SET status='canceled',finished_at=?,rows_done=? WHERE id=? AND status='running'`, now, rows, j.ID)
	case err != nil:
		log.Printf("export %s (%s): %v", j.ID, j.Source, err)
		s.exportRemoveWork(j.ID)
		s.st.exec(`UPDATE export_jobs SET status='failed',finished_at=?,error=?,rows_done=? WHERE id=? AND status='running'`,
			now, clampError(err.Error()), rows, j.ID)
	default:
		s.st.exec(`UPDATE export_jobs SET status='done',finished_at=?,expires_at=?,rows_done=?,bytes=? WHERE id=? AND status='running'`,
			now, now+exportFileTTL.Milliseconds(), rows, bytesOut, j.ID)
	}
	return true
}

func (s *Server) exportRemoveWork(id string) {
	_ = os.Remove(s.exportPartPath(id))
	_ = os.Remove(filepath.Join(s.exportDir(), id+".idx"))
}

// cancelRunning stops the in-flight job, reporting whether this process was running it.
func (r *exportRuntime) cancelRunning(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.cancels[id]; ok {
		c()
		return true
	}
	return false
}

func (s *Server) exportExecuteSafe(ctx context.Context, j *exportJob) (rows, size int64, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("internal error: %v", r)
		}
	}()
	return s.exportExecute(ctx, j)
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	return n, err
}

// exportWriter receives each matching row. raw carries the unrendered database text (the report body
// rides at src.sel's length).
type exportWriter interface {
	write(raw []string, row exportRow) error
	finish() error
}

func (s *Server) exportExecute(ctx context.Context, j *exportJob) (rowsDone, size int64, err error) {
	src := exportSourceByKey(j.Source)
	if src == nil {
		return 0, 0, fmt.Errorf("unknown export source %q", j.Source)
	}
	p := j.params()
	plan, err := s.newExportPlan(src, p.Filter)
	if err != nil {
		return 0, 0, err
	}
	if err := os.MkdirAll(s.exportDir(), 0o755); err != nil {
		return 0, 0, err
	}
	part := s.exportPartPath(j.ID)
	f, err := os.Create(part)
	if err != nil {
		return 0, 0, err
	}
	closed := false
	defer func() {
		if !closed {
			f.Close()
		}
		if err != nil {
			os.Remove(part)
			os.Remove(filepath.Join(s.exportDir(), j.ID+".idx"))
		}
	}()
	cw := &countingWriter{w: f}
	bw := bufio.NewWriterSize(cw, 64<<10)
	plan.withColumns(p.Columns)
	ec := s.newExportCtx(p.TZ, p.Columns)
	ec.priceOf = plan.priceOf
	withBody := j.Format == "md_zip"

	var w exportWriter
	switch j.Format {
	case "csv":
		w, err = newCSVExportWriter(bw, p.Columns)
	case "jsonl":
		w = newJSONLExportWriter(bw, p.Columns)
	case "md_zip":
		w, err = s.newZipExportWriter(bw, src, ec, p, j.ID)
	default:
		err = fmt.Errorf("unknown export format %q", j.Format)
	}
	if err != nil {
		return 0, 0, err
	}

	batchNo := 0
	err = plan.scan(ctx, withBody, exportBatchRows, func(raw []string) error {
		if err := w.write(raw, src.build(ec, raw)); err != nil {
			return err
		}
		rowsDone++
		return nil
	}, func(int) error {
		batchNo++
		if err := bw.Flush(); err != nil {
			return err
		}
		// Progress is advisory: a failed update must not fail the export.
		s.st.exec(`UPDATE export_jobs SET rows_done=?,bytes=? WHERE id=? AND status='running'`, rowsDone, cw.n, j.ID)
		if h := s.exports.afterBatch; h != nil {
			h(j.ID, batchNo)
		}
		return ctx.Err()
	})
	if err != nil {
		return rowsDone, 0, err
	}
	if err = ctx.Err(); err != nil {
		return rowsDone, 0, err
	}
	if err = w.finish(); err != nil {
		return rowsDone, 0, err
	}
	if err = bw.Flush(); err != nil {
		return rowsDone, 0, err
	}
	if err = f.Sync(); err != nil {
		return rowsDone, 0, err
	}
	closed = true
	if err = f.Close(); err != nil {
		return rowsDone, 0, err
	}
	if err = os.Rename(part, s.exportPath(j)); err != nil {
		return rowsDone, 0, err
	}
	return rowsDone, cw.n, nil
}

// ---------- CSV ----------

type csvExportWriter struct {
	cw   *csv.Writer
	cols []string
}

// newCSVExportWriter writes the UTF-8 byte-order mark first: without it Excel opens a UTF-8 CSV as
// the system code page and mangles every non-ASCII character.
func newCSVExportWriter(w io.Writer, cols []string) (*csvExportWriter, error) {
	if _, err := w.Write([]byte("\xef\xbb\xbf")); err != nil {
		return nil, err
	}
	c := &csvExportWriter{cw: csv.NewWriter(w), cols: cols}
	return c, c.cw.Write(cols)
}

func (c *csvExportWriter) write(_ []string, row exportRow) error {
	rec := make([]string, len(c.cols))
	for i, k := range c.cols {
		rec[i] = csvCell(fmtCell(row[k]))
	}
	return c.cw.Write(rec)
}

func (c *csvExportWriter) finish() error {
	c.cw.Flush()
	return c.cw.Error()
}

// csvCell neutralises spreadsheet formula injection: a cell that starts with = + - or @ is evaluated
// by Excel, and several exported columns hold text a user or a remote party controls.
func csvCell(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		// A bare signed number is data, not a formula.
		if _, err := strconv.ParseFloat(v, 64); err == nil {
			return v
		}
		return "'" + v
	}
	return v
}

// ---------- JSON Lines ----------

type jsonlExportWriter struct {
	w    io.Writer
	cols []string
	buf  bytes.Buffer
}

func newJSONLExportWriter(w io.Writer, cols []string) *jsonlExportWriter {
	return &jsonlExportWriter{w: w, cols: cols}
}

func (j *jsonlExportWriter) write(_ []string, row exportRow) error {
	j.buf.Reset()
	j.buf.WriteByte('{')
	for i, k := range j.cols {
		if i > 0 {
			j.buf.WriteByte(',')
		}
		key, _ := json.Marshal(k)
		j.buf.Write(key)
		j.buf.WriteByte(':')
		enc, err := marshalNoEscape(row[k])
		if err != nil {
			return err
		}
		j.buf.Write(enc)
	}
	j.buf.WriteString("}\n")
	_, err := j.w.Write(j.buf.Bytes())
	return err
}

func (j *jsonlExportWriter) finish() error { return nil }

func marshalNoEscape(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// ---------- Markdown ZIP ----------

// frontMatterColumns is the fixed YAML header and the default index.csv column set.
var frontMatterColumns = []string{"id", "symbol", "name", "date", "kind", "rtype", "title", "generator_version", "version"}

type zipExportWriter struct {
	s       *Server
	zw      *zip.Writer
	opts    exportOptions
	cols    []string
	body    int // raw index of the report body
	idxFile *os.File
	idx     *csv.Writer
	idxPath string
	ec      *exportCtx
}

func (s *Server) newZipExportWriter(w io.Writer, src *exportSource, ec *exportCtx, p exportParams, id string) (*zipExportWriter, error) {
	z := &zipExportWriter{s: s, zw: zip.NewWriter(w), opts: p.Options, cols: p.Columns, body: len(src.sel), ec: ec}
	if p.Options.IndexCSV {
		z.idxPath = filepath.Join(s.exportDir(), id+".idx")
		f, err := os.Create(z.idxPath)
		if err != nil {
			return nil, err
		}
		z.idxFile = f
		if _, err := f.Write([]byte("\xef\xbb\xbf")); err != nil {
			return nil, err
		}
		z.idx = csv.NewWriter(f)
		if err := z.idx.Write(append([]string{"path"}, p.Columns...)); err != nil {
			return nil, err
		}
	}
	return z, nil
}

func (z *zipExportWriter) write(raw []string, row exportRow) error {
	rep := reportFromRaw(raw)
	name := z.entryName(rep, row)
	fw, err := z.zw.Create(name)
	if err != nil {
		return err
	}
	if z.opts.FrontMatter {
		if _, err := io.WriteString(fw, frontMatter(row)); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(fw, raw[z.body]); err != nil {
		return err
	}
	if z.idx != nil {
		rec := make([]string, 0, len(z.cols)+1)
		rec = append(rec, csvCell(name))
		for _, k := range z.cols {
			rec = append(rec, csvCell(fmtCell(row[k])))
		}
		return z.idx.Write(rec)
	}
	return nil
}

// entryName is layout/date_symbol_title_version_id.md. The id comes last and is never truncated, so
// two reports can never share a name however long their titles are.
func (z *zipExportWriter) entryName(rep Rep, row exportRow) string {
	title := z.s.repDisplayTitle(&rep)
	base := strings.Join(nonEmptyStrings(rep.Date, rep.Symbol, title, fmtCell(row["generator_version"])), "_")
	file := truncateUTF8Bytes(sanitizeFilename(base), 200) + "_" + strconv.FormatInt(rep.ID, 10) + ".md"
	switch z.opts.Layout {
	case "date":
		d := rep.Date
		if d == "" {
			d = "undated"
		}
		return sanitizeFilename(d) + "/" + file
	case "symbol":
		sym := rep.Symbol
		if sym == "" {
			sym = "_no-symbol"
		}
		return sanitizeFilename(sym) + "/" + file
	}
	return file
}

// frontMatter renders the YAML header. Every string is a double-quoted scalar (a JSON string is one),
// so colons, quotes, leading dashes and newlines in a title cannot break the document.
func frontMatter(row exportRow) string {
	var b strings.Builder
	b.WriteString("---\n")
	for _, k := range frontMatterColumns {
		b.WriteString(k + ": ")
		switch v := row[k].(type) {
		case int64:
			b.WriteString(strconv.FormatInt(v, 10))
		default:
			enc, _ := marshalNoEscape(fmtCell(v))
			b.Write(enc)
		}
		b.WriteByte('\n')
	}
	b.WriteString("---\n\n")
	return b.String()
}

func (z *zipExportWriter) finish() error {
	if z.idx != nil {
		z.idx.Flush()
		if err := z.idx.Error(); err != nil {
			return err
		}
		if _, err := z.idxFile.Seek(0, io.SeekStart); err != nil {
			return err
		}
		fw, err := z.zw.Create("index.csv")
		if err != nil {
			return err
		}
		if _, err := io.Copy(fw, z.idxFile); err != nil {
			return err
		}
		z.idxFile.Close()
		os.Remove(z.idxPath)
		z.idx = nil
	}
	return z.zw.Close()
}

func nonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func truncateUTF8Bytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// ---------- expiry ----------

var exportOrphanName = regexp.MustCompile(`^[0-9a-f]{32}\.(csv|jsonl|zip)$`)

// exportSweep deletes expired files (marking their jobs expired), drops job rows past the retention
// window, and removes finished files that no job row owns. Run hourly.
func (s *Server) exportSweep(now time.Time) {
	ms := now.UnixMilli()
	if rows, err := s.st.query(`SELECT `+exportJobCols+` FROM export_jobs WHERE status='done' AND expires_at>0 AND expires_at<=?`, ms); err == nil {
		var due []*exportJob
		for rows.Next() {
			if j, err := scanExportJob(rows.Scan); err == nil {
				due = append(due, j)
			}
		}
		rows.Close()
		for _, j := range due {
			_ = os.Remove(s.exportPath(j))
			s.st.exec(`UPDATE export_jobs SET status='expired' WHERE id=? AND status='done'`, j.ID)
		}
	}
	cutoff := now.Add(-exportRowTTL).UnixMilli()
	if rows, err := s.st.query(`SELECT `+exportJobCols+` FROM export_jobs WHERE created_at<? AND status NOT IN ('queued','running')`, cutoff); err == nil {
		var old []*exportJob
		for rows.Next() {
			if j, err := scanExportJob(rows.Scan); err == nil {
				old = append(old, j)
			}
		}
		rows.Close()
		for _, j := range old {
			_ = os.Remove(s.exportPath(j))
			s.st.exec(`DELETE FROM export_jobs WHERE id=?`, j.ID)
		}
	}
	entries, err := os.ReadDir(s.exportDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if !exportOrphanName.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < time.Hour {
			continue // a file this young may belong to a job being renamed into place
		}
		id := strings.SplitN(e.Name(), ".", 2)[0]
		if s.st.getExportJob(id) == nil {
			_ = os.Remove(filepath.Join(s.exportDir(), e.Name()))
		}
	}
}

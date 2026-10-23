package app

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/version"
)

// Backup and restore for the whole database, as two CLI subcommands.
//
// Everything the portal owns lives in the database — accounts, groups, reports, apps, tokens,
// settings — and until now there was no way to take a copy of it or put one back. The Docker stack
// keeps Postgres in a named volume, so `docker compose down -v` (the documented way to reset a
// stack) deletes it, and `pg_dump` is not reachable from the portal's own image.
//
// It is deliberately ONE format for both drivers rather than a wrapper around pg_dump/`.dump`:
//   - the same command works whichever driver a deployment runs, so the runbook has one line in it;
//   - a dump is portable BETWEEN drivers, which makes "outgrew SQLite, move to Postgres" a supported
//     move rather than a rewrite;
//   - it needs no client binaries in the image (the release image carries the portal and nothing else).
//
// The cost is that it is a logical dump, not a physical one: it restores into the schema the running
// binary creates, so a backup taken by a NEWER build can carry a column this build has never heard
// of. That case is a hard error naming the column, never a silent drop — see restoreStream.
//
// The file is JSON Lines: one header object, then, per table, a `table` object followed by its
// `row` objects. Line-oriented so a dump streams in constant memory in both directions and stays
// greppable; `-` means stdin/stdout, so `backup - | gzip` and `zcat … | restore -` compose.

const (
	backupFormat        = "report-portal-backup"
	backupFormatVersion = 1
)

// backupHeader is the first line of a dump. Driver and app version are recorded for the operator
// reading a file months later, not for validation: a dump restores across drivers by design.
type backupHeader struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	// SchemaVersion is the database's generation marker (ADR 0013), and it is in the HEADER rather
	// than left to be discovered among the `meta` rows for a reason: a restore has to be able to
	// refuse a dump BEFORE it deletes anything, and `meta` sorts into the middle of the file.
	SchemaVersion int      `json:"schema_version"`
	CreatedAt     string   `json:"created_at"`
	Driver        string   `json:"driver"`
	AppVersion    string   `json:"app_version"`
	Tables        []string `json:"tables"`
	// Migrations is the migration ledger the release that wrote this dump had applied — the steps
	// this DATA satisfies, not a diagnostic note. A restore validates it (checkDumpMigrations) and
	// uses it to decide which columns the dump is allowed to be missing, so it is a claim the content
	// has to bear out.
	Migrations []string `json:"migrations"`
}

// backupSection introduces one table. Columns are recorded per dump rather than assumed, so a file
// stays readable after the schema grows a column.
type backupSection struct {
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
}

// b64Key tags a binary value inside a row. Only app_files.content is binary today; the tag exists
// so that stays true by construction rather than by luck.
const b64Key = "$b64"

// backupTables lists every table in the current schema, from the schema declarations and the
// migration list themselves so a new table is included the day it is declared and nobody has to
// remember a second list.
func (s *Store) backupTables() []string {
	seen := map[string]bool{}
	var base []string
	for _, stmt := range s.baseSchemaStmts() {
		t, _, ok := parseCreateTable(stmt)
		if !ok || seen[t] {
			continue
		}
		seen[t] = true
		base = append(base, t)
	}
	return backupTableNames(base, s.migrations())
}

// Backup writes a full dump of the configured database to path ("-" for stdout).
func Backup(cfgPath, path string) (tables int, rows int64, err error) {
	c, err := config.EnsureConfig(cfgPath)
	if err != nil {
		return 0, 0, fmt.Errorf("config: %w", err)
	}
	st, err := OpenStore(c.DBDriver, c.DBSource())
	if err != nil {
		return 0, 0, err
	}
	defer st.Close()

	out := io.Writer(os.Stdout)
	if path != "-" {
		// 0600: a dump carries password hashes, API token hashes and the sealed SSO keyring. The
		// SSO secrets are useless without config secret_key, which is NOT in here — but the rest is
		// plenty, so the file must not be readable by anyone else.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return 0, 0, err
		}
		defer f.Close()
		// The mode above applies only when O_CREATE actually creates the file. Overwriting an
		// existing one keeps whatever mode it already had — which is precisely the shape of a
		// nightly script writing to the same path forever, so the guarantee has to be made again
		// here or it is a guarantee about the first run only.
		//
		// Fatal rather than a warning: the promise is that this file is not readable by others, and
		// a backup tool that quietly writes password hashes world-readable because a chmod failed
		// has broken the one property it was asked for. A warning in a cron job is not read.
		if err := f.Chmod(0o600); err != nil {
			return 0, 0, fmt.Errorf("cannot make %s readable only by you (it carries password and "+
				"token hashes): %w", path, err)
		}
		out = f
	}
	w := bufio.NewWriterSize(out, 1<<16)
	tables, rows, err = st.dumpTo(w)
	if err != nil {
		return tables, rows, err
	}
	return tables, rows, w.Flush()
}

// dumpTo writes the whole database as JSON Lines inside ONE read transaction, so the dump is a
// single point in time rather than a table-by-table sample of a moving database.
func (s *Store) dumpTo(w io.Writer) (int, int64, error) {
	names := s.backupTables()
	opts := &sql.TxOptions{ReadOnly: true}
	if s.driver == "postgres" {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.db.BeginTx(context.Background(), opts)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback() //nolint:errcheck // read-only: rollback is the only ending

	enc := json.NewEncoder(w)
	if err := enc.Encode(backupHeader{
		Format:        backupFormat,
		Version:       backupFormatVersion,
		SchemaVersion: schemaVersionTx(tx),
		CreatedAt:     time.Now().Format(time.RFC3339),
		Driver:        s.driver,
		AppVersion:    version.Version,
		Tables:        names,
		Migrations:    s.migrationIdsTx(tx),
	}); err != nil {
		return 0, 0, err
	}

	var total int64
	for _, table := range names {
		n, err := dumpTable(tx, enc, table)
		if err != nil {
			return 0, total, fmt.Errorf("dump %s: %w", table, err)
		}
		total += n
	}
	return len(names), total, nil
}

// schemaVersionTx reads the generation marker ON THE TRANSACTION, not through the pool.
//
// Store.schemaVersion() would be the obvious call and it deadlocks: on SQLite the pool is a single
// connection, this function's own transaction is holding it, and a pooled read would wait for a
// connection it can never get. Reading it here also makes the header describe the same snapshot as
// the rows underneath it, which is what a dump is for.
func schemaVersionTx(tx *sql.Tx) int {
	var v sql.NullString
	if err := tx.QueryRow("SELECT v FROM meta WHERE k='schema_version'").Scan(&v); err != nil {
		return 0 // unknown rather than guessed; the restore treats 0 as "not recorded"
	}
	if n, err := strconv.Atoi(v.String); err == nil && n > 0 {
		return n
	}
	return 0
}

// dumpTable streams one table. Every statement goes through tx, never the pool: on SQLite the pool
// is one connection, so a pooled read while this transaction is open would wait for a connection
// the transaction itself is holding, and hang for ever.
func dumpTable(tx *sql.Tx, enc *json.Encoder, table string) (int64, error) {
	// Table names come from the schema declaration, never from input, so interpolating is safe —
	// and neither driver accepts an identifier as a bind parameter.
	rows, err := tx.Query("SELECT * FROM " + table)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		return 0, err
	}
	binary := make([]bool, len(cols))
	for i, ct := range types {
		switch strings.ToUpper(ct.DatabaseTypeName()) {
		case "BLOB", "BYTEA":
			binary[i] = true
		}
	}
	if err := enc.Encode(backupSection{Table: table, Columns: cols}); err != nil {
		return 0, err
	}

	scan := make([]any, len(cols))
	holders := make([]any, len(cols))
	for i := range scan {
		holders[i] = &scan[i]
	}
	var n int64
	line := struct {
		Row []any `json:"row"`
	}{Row: make([]any, len(cols))}
	for rows.Next() {
		if err := rows.Scan(holders...); err != nil {
			return n, err
		}
		for i, v := range scan {
			line.Row[i] = backupValue(v, binary[i])
		}
		if err := enc.Encode(line); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

// backupValue makes one scanned column JSON-representable without losing what it was.
//
// The binary flag comes from the column's declared type, not from the Go type that came back: both
// drivers hand back []byte for real binary, but a text column that ever arrives as []byte must go
// back as text, or restoring it into SQLite would turn a string into a blob.
func backupValue(v any, binary bool) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		if binary {
			return map[string]string{b64Key: base64.StdEncoding.EncodeToString(t)}
		}
		return string(t)
	case time.Time:
		return t.Format(time.RFC3339Nano)
	default:
		return v // int64, float64, bool, string — all round-trip through JSON as themselves
	}
}

// restoreValue reverses backupValue. Numbers arrive as json.Number (the decoder is told to keep
// them) because a report id past 2^53 would otherwise come back as a different number through
// float64 — silent, and exactly the kind of corruption a restore must not introduce.
func restoreValue(v any) (any, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i, nil
		}
		return t.Float64()
	case map[string]any:
		enc, ok := t[b64Key].(string)
		if !ok {
			return nil, fmt.Errorf("unrecognised object value %v", t)
		}
		return base64.StdEncoding.DecodeString(enc)
	default:
		return v, nil // string, bool
	}
}

// RestoreReport is what a restore did, or (without force) what it would do.
type RestoreReport struct {
	Header   backupHeader
	Rows     map[string]int64 // rows read from the dump, per table
	Existing map[string]int64 // rows already in the target, per table — all of them are replaced
	Total    int64
	Replaced int64
	Applied  bool
}

// Restore loads a dump into the configured database, replacing everything in it.
//
// Without force it is a dry run: the file is parsed and validated end to end and the counts are
// reported, but nothing is written. That makes "is this backup actually readable?" a question an
// operator can answer without a spare database, and it makes the destructive form explicit rather
// than a flag people learn about afterwards.
func Restore(cfgPath, path string, force bool) (*RestoreReport, error) {
	c, err := config.EnsureConfig(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	os.MkdirAll(config.DirOf(c.DBPath), 0o755)
	st, err := OpenStore(c.DBDriver, c.DBSource())
	if err != nil {
		return nil, err
	}
	defer st.Close()

	in := io.Reader(os.Stdin)
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		in = f
	}
	return st.restoreFrom(bufio.NewReaderSize(in, 1<<16), force)
}

func (s *Store) restoreFrom(r io.Reader, force bool) (*RestoreReport, error) {
	// Every pooled read happens HERE, before any transaction is opened: on SQLite the pool is a
	// single connection, and a pooled query issued while the restore transaction is open would
	// block on the connection that transaction is holding.
	known := map[string]map[string]bool{}
	for _, t := range s.backupTables() {
		known[t] = map[string]bool{}
	}
	if err := s.collectColumns(known); err != nil {
		return nil, err
	}
	existing, err := s.rowCounts(s.backupTables())
	if err != nil {
		return nil, err
	}
	identity, err := s.identityColumns()
	if err != nil {
		return nil, err
	}
	// The values a restore must reconcile rather than simply replace. Read HERE, through the pool,
	// because the restore's own transaction is about to delete the `meta` rows that hold them
	// (§3.3). `forecast_population_from` is write-once and must never move silently, so a dump that
	// disagrees with the live value is refused; a dump that lacks it inherits the live one.
	prior := s.captureRestorePrior()

	rep := &RestoreReport{
		Rows:     map[string]int64{},
		Existing: existing,
	}
	for _, n := range existing {
		rep.Replaced += n
	}

	dec := json.NewDecoder(r)
	dec.UseNumber()
	if err := dec.Decode(&rep.Header); err != nil {
		return nil, fmt.Errorf("read backup header: %w", err)
	}
	if rep.Header.Format != backupFormat {
		return nil, fmt.Errorf("not a report-portal backup (format %q)", rep.Header.Format)
	}
	if rep.Header.Version > backupFormatVersion {
		return nil, fmt.Errorf("backup format v%d is newer than this build understands (v%d): upgrade the portal first",
			rep.Header.Version, backupFormatVersion)
	}
	if err := checkSchemaGeneration(rep.Header.SchemaVersion); err != nil {
		return nil, err
	}
	// The dump's own migration level, checked before anything is deleted: which steps its data
	// satisfies decides both whether this build can load it at all, and which columns it is allowed to
	// be missing.
	steps := s.migrations()
	if err := checkDumpMigrations(rep.Header.Migrations, steps); err != nil {
		return nil, err
	}
	optional := optionalColumns(rep.Header.Migrations, steps)
	renamed := renamesNotCarried(rep.Header.Migrations, steps)

	var tx *sql.Tx
	if force {
		tx, err = s.db.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback() //nolint:errcheck // no-op once committed
		// Empty EVERY table in the schema, not only the ones the dump carries: a table the dump
		// omits must end up empty too, or the result is a mix of two databases that never existed.
		for _, t := range s.backupTables() {
			if _, err := tx.Exec("DELETE FROM " + t); err != nil {
				return nil, fmt.Errorf("clear %s: %w", t, err)
			}
		}
	}
	if err := s.restoreStream(dec, tx, known, optional, renamed, identity, rep); err != nil {
		return nil, err
	}
	if !force {
		return rep, nil
	}
	if err := s.resetIdentities(tx, identity, rep.Rows); err != nil {
		return nil, err
	}
	// The database's own consistency is settled HERE, inside the transaction that wrote the rows: a
	// restore that ended with the data and the ledger disagreeing would be a database nobody could
	// reason about, and leaving a step unrecorded is how the next start re-runs an upgrade over data
	// that has already been upgraded.
	if err := s.finishRestore(tx, steps, prior); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	rep.Applied = true
	return rep, nil
}

// restorePrior holds the live values a restore must reconcile against the dump rather than replace
// outright. It is captured before the restore's transaction clears the tables.
type restorePrior struct {
	populationFrom string // the live forecast_population_from, "" when unset
	autoMode       string // the live forecast_auto, "" when unset
	readerStage    string // the live reading stage as the readers resolve it: off, card or full
}

// captureRestorePrior reads the live values a restore must reconcile. It runs on the pool, before the
// restore transaction opens.
func (s *Store) captureRestorePrior() restorePrior {
	return restorePrior{
		populationFrom: s.populationFromRow(),
		autoMode:       s.GetSetting(setForecastAuto, ""),
		readerStage:    storedForecastReaderStage(s.poolExec()),
	}
}

// populationFromRow reads the raw stored ledger start without going through GetSetting's parse, so a
// malformed value is still visible to the restore conflict check.
func (s *Store) populationFromRow() string {
	var v sql.NullString
	if err := s.queryRow(`SELECT v FROM meta WHERE k=?`, setForecastPopulationFrom).Scan(&v); err != nil {
		return ""
	}
	return v.String
}

// finishRestore settles the database after its rows have been replaced: the unknown-step check, the
// product check, and the ledger. All of it runs on the restore's own transaction, so a failure here
// takes the data back with it rather than leaving a half-loaded database behind.
func (s *Store) finishRestore(tx *sql.Tx, steps []migration, prior restorePrior) error {
	e := migExec{s: s, ex: tx}
	// The write-once keys. `meta` was just replaced by the dump's, so a key the dump lacked is now
	// absent and one it carried is now the dump's value. Both rules run on the tx handle only.
	if err := applyRestorePrior(e, prior); err != nil {
		return err
	}
	known := map[string]bool{}
	for _, m := range steps {
		known[m.id] = true
	}
	// A dump from a release that had steps this build does not: its `meta` carried the record in.
	if err := refuseUnknownLedgerRowsExec(e, known); err != nil {
		return err
	}
	// What the schema actually looks like now, not what the dump claimed.
	if err := s.verifyMigrationProducts(e, steps); err != nil {
		return err
	}
	// Re-record. The restored `meta` came from whichever release wrote the dump, so the ledger in it
	// describes that release's view — this build's steps are what the database has just been checked
	// against, and a missing row would make the next start re-run a step whose work is already done.
	for _, m := range steps {
		if e.setting(migrationLedgerKey(m.id)) != "" {
			continue
		}
		if err := e.setSetting(migrationLedgerKey(m.id), time.Now().UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return nil
}

// applyRestorePrior enforces the restore rules for the write-once keys (doc §3.3). The dump's `meta`
// has already replaced the live one, so:
//
//   - the ledger start is carried from the live database when the dump lacks it (an older build wrote
//     that dump, or the key was set after the backup was taken);
//   - a dump and a live database that both set it MUST agree — the key is write-once and a restore
//     must never move it, so a disagreement is refused rather than resolved in either direction.
func applyRestorePrior(e migExec, prior restorePrior) error {
	dumpVal := e.setting(setForecastPopulationFrom)
	switch {
	case prior.populationFrom == "" && dumpVal == "":
		// Neither side set it: nothing to do.
	case prior.populationFrom == "":
		// The live database had none; the dump's value stands as restored.
	case dumpVal == "":
		if err := e.setSetting(setForecastPopulationFrom, prior.populationFrom); err != nil {
			return fmt.Errorf("carry the ledger start through the restore: %w", err)
		}
	case dumpVal != prior.populationFrom:
		// Compare INSTANTS, not text: the same instant can be written with a different but equivalent
		// RFC3339 offset, and refusing that would make a harmless restore impossible.
		dv, derr := time.Parse(time.RFC3339, dumpVal)
		pv, perr := time.Parse(time.RFC3339, prior.populationFrom)
		if derr == nil && perr == nil && dv.Equal(pv) {
			break
		}
		return fmt.Errorf("forecast_population_conflict: dump has ledger start %q, live database has %q",
			dumpVal, prior.populationFrom)
	}
	// The unrecorded floor (P2): if the boundary is set but the dump lacks the floor keys, derive and
	// persist them on this transaction. A dump that carries them keeps them.
	if raw := e.setting(setForecastPopulationFrom); raw != "" && e.setting(setForecastUnrecordedFloorID) == "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			if err := storeUnrecordedFloor(e, t); err != nil {
				return fmt.Errorf("derive the unrecorded floor through the restore: %w", err)
			}
		}
	}
	if err := applyRestoreEvaluator(e, prior.autoMode); err != nil {
		return err
	}
	if err := applyRestoreReaderStage(e, prior.readerStage); err != nil {
		return err
	}
	// A restored older dump brings every forecast_scope back NULL; the Default OU is normalised to
	// none here, on the same transaction, exactly as at boot (P38).
	if err := ensureDefaultForecastScope(e); err != nil {
		return fmt.Errorf("normalise the Default OU scope on restore: %w", err)
	}
	return nil
}

// applyRestoreEvaluator applies §3.3's rules for the evaluator's own keys.
//
// A restored mode must never be stronger than the weaker of the two databases, and it must never be
// 1 without a gate: the gates are reset (they describe hand checks of a database that no longer
// exists), and mode 1 with no gate plans every set and writes none of them. Both rules run on the
// restore's transaction, and each real change leaves an audit row attributed to `system`.
func applyRestoreEvaluator(e migExec, live string) error {
	for _, k := range []string{setForecastEvalState, setForecastAlertState, setForecastDryGate, setForecastAlertTestOKAt} {
		if _, err := e.exec(`DELETE FROM meta WHERE k=?`, k); err != nil {
			return fmt.Errorf("reset %s on restore: %w", k, err)
		}
	}
	dump := forecastMode(e.setting(setForecastAuto))
	liveMode := forecastMode(live)
	final := lowerForecastMode(dump, liveMode)
	if final != dump {
		if err := e.setSetting(setForecastAuto, final); err != nil {
			return fmt.Errorf("cap the evaluator mode through the restore: %w", err)
		}
		if err := auditRestoreChange(e, AuditForecastAuto, setForecastAuto, map[string]any{"from": dump, "to": final}); err != nil {
			return err
		}
	}
	if final == forecastModeOn {
		// The gates were just deleted, so mode 1 would plan everything and write nothing. Cap it and
		// say why, which is the only way an operator learns the mode did not survive the restore.
		if err := e.setSetting(setForecastAuto, forecastModeDry); err != nil {
			return fmt.Errorf("cap the evaluator mode at dry: %w", err)
		}
		if err := auditRestoreChange(e, AuditForecastAuto, setForecastAuto, map[string]any{
			"from": forecastModeOn, "to": forecastModeDry, "reason": "restore_gates_empty",
		}); err != nil {
			return err
		}
	}
	return nil
}

// applyRestoreReaderStage applies §3.3's rule for the reading stage: a restore never raises it. The
// result is the lower of the dump and the live database in off < card < full. Both sides are
// resolved the way the readers resolve them (see storedForecastReaderStage). A real change from the
// restored value leaves an audit row attributed to `system`, so an operator can see that the stage
// did not survive the restore as dumped.
func applyRestoreReaderStage(e migExec, live string) error {
	dump := storedForecastReaderStage(e)
	final := lowerForecastReaderStage(dump, live)
	if final == dump {
		return nil
	}
	if err := e.setSetting(setForecastReader, final); err != nil {
		return fmt.Errorf("cap the reading stage through the restore: %w", err)
	}
	return auditRestoreChange(e, AuditForecastReader, setForecastReader, map[string]any{"from": dump, "to": final})
}

// storedForecastReaderStage is the stage the stored forecast_reader row stands for, resolved as the
// readers resolve it: no row or a NULL is the default (card), a stored value is itself when it is a
// stage and off otherwise. A stored empty string is therefore off, not unset. A read that fails is
// off as well, because a restore that cannot see the stage must not guess a higher one.
func storedForecastReaderStage(e migExec) string {
	var v sql.NullString
	switch err := e.queryRow(`SELECT v FROM meta WHERE k=?`, setForecastReader).Scan(&v); {
	case errors.Is(err, sql.ErrNoRows):
		return "card"
	case err != nil:
		return "off"
	case !v.Valid:
		return "card"
	case validForecastReaderStage(v.String):
		return v.String
	default:
		return "off"
	}
}

// lowerForecastReaderStage orders off < card < full.
func lowerForecastReaderStage(a, b string) string {
	rank := func(stage string) int {
		switch stage {
		case "full":
			return 2
		case "card":
			return 1
		default:
			return 0
		}
	}
	if rank(a) <= rank(b) {
		return a
	}
	return b
}

// lowerForecastMode orders off < dry < 1.
func lowerForecastMode(a, b string) string {
	rank := func(m string) int {
		switch m {
		case forecastModeOn:
			return 2
		case forecastModeDry:
			return 1
		default:
			return 0
		}
	}
	if rank(a) <= rank(b) {
		return a
	}
	return b
}

// auditRestoreChange writes the restore's own audit row through the restore transaction, so a
// rolled-back restore does not leave a claim about a change that did not happen.
func auditRestoreChange(e migExec, action, key string, detail map[string]any) error {
	body, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,?,?,?,?,?,?,?)`, time.Now().UTC().Format(time.RFC3339), "system", 0,
		action, "settings", key, string(body), "")
	return err
}

// restoreStream reads the dump body. With tx == nil it validates and counts without writing, which
// is the dry run; the parsing and the column checks are identical either way, so a dry run that
// passes is real evidence about the file.
func (s *Store) restoreStream(dec *json.Decoder, tx *sql.Tx, known, optional map[string]map[string]bool, renamed map[string]map[string]string, identity map[string]string, rep *RestoreReport) error {
	var (
		table  string
		cols   []string
		stmt   *sql.Stmt
		values []any
	)
	closeStmt := func() error {
		if stmt == nil {
			return nil
		}
		err := stmt.Close()
		stmt = nil
		return err
	}
	defer closeStmt() //nolint:errcheck

	for {
		var line struct {
			Table   string   `json:"table"`
			Columns []string `json:"columns"`
			Row     []any    `json:"row"`
		}
		if err := dec.Decode(&line); err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("read backup: %w", err)
		}
		if line.Table != "" {
			if err := closeStmt(); err != nil {
				return err
			}
			table, cols = line.Table, line.Columns
			// A dump from before a step that renamed a baseline column carries the old name. The
			// rename moves no data, so the value is loaded under the new one; a dump that claims the
			// step gets no translation, and an old name in it is refused below as unknown.
			//
			// The dump must carry the old name and only the old name: its header says it predates the
			// step, so a new name in it — alone or beside the old one — is a header and content that
			// disagree. Beside the old one it would also put the column in the insert twice, which
			// SQLite accepts by silently keeping the first value.
			for from, to := range renamed[table] {
				if containsString(cols, to) {
					return fmt.Errorf("the backup's %s carries %s, but its header says it predates the step "+
						"that renamed %s to it: the backup is damaged or was edited by hand", table, to, from)
				}
			}
			for i, c := range cols {
				if to, ok := renamed[table][c]; ok {
					cols[i] = to
				}
			}
			cset, ok := known[table]
			if !ok {
				return fmt.Errorf("the backup contains table %q, which this build has no schema for: "+
					"restore it with the version that wrote it (%s), or upgrade this one", table, rep.Header.AppVersion)
			}
			for _, c := range cols {
				if !cset[c] {
					return fmt.Errorf("the backup's %s.%s does not exist in this build's schema: "+
						"restoring would silently drop it — upgrade the portal to at least %s first",
						table, c, firstNonEmpty(rep.Header.AppVersion, "the version that wrote the backup"))
				}
			}
			// Every column the current schema declares has to be in the dump — EXCEPT the ones added
			// by a migration the dump predates, which the header named and optionalColumns allows. A
			// missing column used to be tolerated for everything, which is how a restore could
			// succeed while silently producing rows that never had the data; a missing column is now
			// either declared absent by the dump's own migration level, or the refusal stands.
			var missing []string
			for c := range cset {
				if !containsString(cols, c) && !optional[table][c] {
					missing = append(missing, c)
				}
			}
			if len(missing) > 0 {
				sort.Strings(missing)
				return fmt.Errorf("the backup's %s is missing %s, so it was written by an older schema: "+
					"restore it with the release that wrote it, then back it up again",
					table, strings.Join(missing, ", "))
			}
			values = make([]any, len(cols))
			if tx != nil {
				var err error
				if stmt, err = s.prepareInsert(tx, table, cols, identity); err != nil {
					return fmt.Errorf("prepare insert into %s: %w", table, err)
				}
			}
			continue
		}
		if table == "" {
			return fmt.Errorf("backup has a row before any table header")
		}
		if len(line.Row) != len(cols) {
			return fmt.Errorf("%s: row has %d values for %d columns", table, len(line.Row), len(cols))
		}
		for i, v := range line.Row {
			conv, err := restoreValue(v)
			if err != nil {
				return fmt.Errorf("%s.%s: %w", table, cols[i], err)
			}
			values[i] = conv
		}
		if stmt != nil {
			if _, err := stmt.Exec(values...); err != nil {
				return fmt.Errorf("insert into %s: %w", table, err)
			}
		}
		rep.Rows[table]++
		rep.Total++
	}
	return closeStmt()
}

// prepareInsert builds the per-table insert. On Postgres an identity column rejects an explicit
// value unless the statement says OVERRIDING SYSTEM VALUE — and a restore must keep the original
// ids, because half the database refers to reports and groups by them.
func (s *Store) prepareInsert(tx *sql.Tx, table string, cols []string, identity map[string]string) (*sql.Stmt, error) {
	marks := make([]string, len(cols))
	for i := range marks {
		marks[i] = "?"
	}
	override := ""
	if id, ok := identity[table]; ok && containsString(cols, id) {
		override = " OVERRIDING SYSTEM VALUE"
	}
	q := fmt.Sprintf("INSERT INTO %s (%s)%s VALUES (%s)", table, strings.Join(cols, ","), override, strings.Join(marks, ","))
	return tx.Prepare(s.bind(q))
}

// identityColumns maps table -> identity column for Postgres. SQLite's AUTOINCREMENT accepts explicit
// ids and advances its own counter on insert, so it needs neither the override nor the reset below.
func (s *Store) identityColumns() (map[string]string, error) {
	out := map[string]string{}
	if s.driver != "postgres" {
		return out, nil
	}
	rows, err := s.db.Query(`SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema='public' AND is_identity='YES'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t, c string
		if err := rows.Scan(&t, &c); err != nil {
			return nil, err
		}
		out[t] = c
	}
	return out, rows.Err()
}

// resetIdentities moves each Postgres identity sequence past the ids just restored. Without it the
// sequence still sits at 1 and the very next insert collides with a restored row.
func (s *Store) resetIdentities(tx *sql.Tx, identity map[string]string, restored map[string]int64) error {
	for table, col := range identity {
		if restored[table] == 0 {
			continue
		}
		q := fmt.Sprintf(
			`SELECT setval(pg_get_serial_sequence('%s','%s'), COALESCE((SELECT MAX(%s) FROM %s),0)+1, false)`,
			table, col, col, table)
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("reset %s.%s sequence: %w", table, col, err)
		}
	}
	return nil
}

// collectColumns fills in the real column set of every table, which is what decides whether a dump
// can be loaded without dropping anything.
func (s *Store) collectColumns(into map[string]map[string]bool) error {
	for table := range into {
		rows, err := s.db.Query("SELECT * FROM " + table + " WHERE 1=0")
		if err != nil {
			return fmt.Errorf("inspect %s: %w", table, err)
		}
		cols, err := rows.Columns()
		rows.Close()
		if err != nil {
			return err
		}
		for _, c := range cols {
			into[table][c] = true
		}
	}
	return nil
}

func (s *Store) rowCounts(tables []string) (map[string]int64, error) {
	out := map[string]int64{}
	for _, t := range tables {
		var n int64
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + t).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", t, err)
		}
		if n > 0 {
			out[t] = n
		}
	}
	return out, nil
}

// checkSchemaGeneration refuses a dump this build cannot represent.
//
// It runs before the transaction is opened. That is cheapness, not safety — the rollback is what
// keeps a failed restore from leaving a partial database, whatever the reason — but there is no
// sense emptying 36 tables to then throw the work away.
//
// Without it a cross-generation restore "succeeds": the tables were created by the running binary in
// its own shape, the rows arrive from an older or newer one, and the dump's schema_version lands in
// `meta`. Nothing complains until the NEXT boot, where the startup boundary refuses — a delayed
// failure after a destructive operation, and the worst possible order for the two. That boundary
// cannot catch this itself: it runs at open time against the TARGET database, which at that moment is
// empty or current, and it never sees the dump at all.
//
// The dump has to carry the current baseline. A missing field, an older value and a newer one are
// all refusals: the field shipped with the format itself, so a dump without it was not written by any
// release of this feature. Accepting one was the permissive branch that existed only to keep legacy
// dumps loading, and a database from an older line is taken through that line's last release instead
// — see the boundary in migrate.go.
func checkSchemaGeneration(dump int) error {
	switch {
	case dump == schemaBaseline:
		return nil
	case dump == 0:
		return fmt.Errorf("this backup records no schema generation, so this build cannot tell which " +
			"release wrote it: restore it with the release that wrote it and back it up again")
	case dump < schemaBaseline:
		return fmt.Errorf("this backup is from schema generation %d and the portal now requires %d: "+
			"restore it with the release that wrote it, take the database up to %s, then back it up "+
			"again", dump, schemaBaseline, legacyBridge)
	default:
		return fmt.Errorf("this backup is from schema generation %d, which is newer than this build's %d: "+
			"upgrade the portal before restoring", dump, schemaBaseline)
	}
}

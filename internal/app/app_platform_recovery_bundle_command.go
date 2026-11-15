package app

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

// The operator command for the platform recovery bundle (doc 168, step 13):
//
//	report-portal recovery-bundle write <dir> --checkpoints <dir-or-archive>... (--runner-journal <file> | --no-runner-journal)
//	report-portal recovery-bundle restore <dir> [--force]
//
// `write` runs on the old host and produces the directory an operator carries to a fresh one; `restore` runs on
// the fresh host and loads it. Paths come from flags because config.yaml holds infrastructure only. The restore
// side adds no semantics of its own: it verifies the bundle, places the managed image blobs, and loads the
// Portal dump through the same code as `report-portal restore`, which leaves the Portal in recovery mode with
// every runtime binding retired. The checkpoint archives and the runner journal are not loaded by this command:
// they stay in the bundle for the runner host and for the Portal's own import review.

const recoveryBundleUsage = `usage:
  report-portal recovery-bundle write <dir> --checkpoints <dir-or-archive> [--checkpoints ...] (--runner-journal <file> | --no-runner-journal)
  report-portal recovery-bundle restore <dir> [--force [--replace-existing]]   (without --force it is a dry run)`

// recoveryBundleHost names the files on the old host that the bundle carries beside the database.
type recoveryBundleHost struct {
	// Checkpoints are directories to search for <checkpoint id>.tar (the checkpoint root, or its
	// .portal-checkpoints directory), or archive files named that way.
	Checkpoints []string
	// RunnerJournal is the old runner's journal file; NoRunnerJournal states that the host has none to carry.
	RunnerJournal   string
	NoRunnerJournal bool
}

type recoveryBundleArgs struct {
	Action string
	Dir    string
	Host   recoveryBundleHost
	Force  bool
	// ReplaceExisting allows --force over a database that already holds data.
	ReplaceExisting bool
}

type repeatedFlag []string

func (r *repeatedFlag) String() string     { return strings.Join(*r, ",") }
func (r *repeatedFlag) Set(v string) error { *r = append(*r, v); return nil }

// parseRecoveryBundleArgs reads `<action> <dir> [flags]`. The directory may stand anywhere among the flags.
func parseRecoveryBundleArgs(args []string) (recoveryBundleArgs, error) {
	var parsed recoveryBundleArgs
	if len(args) == 0 {
		return parsed, fmt.Errorf("%s", recoveryBundleUsage)
	}
	parsed.Action = args[0]
	if parsed.Action != "write" && parsed.Action != "restore" {
		return parsed, fmt.Errorf("unknown recovery-bundle action %q\n%s", parsed.Action, recoveryBundleUsage)
	}
	fs := flag.NewFlagSet("recovery-bundle "+parsed.Action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var checkpoints repeatedFlag
	var journal string
	var noJournal, force, replace bool
	if parsed.Action == "write" {
		fs.Var(&checkpoints, "checkpoints", "")
		fs.StringVar(&journal, "runner-journal", "", "")
		fs.BoolVar(&noJournal, "no-runner-journal", false, "")
	} else {
		fs.BoolVar(&force, "force", false, "")
		fs.BoolVar(&replace, "replace-existing", false, "")
	}
	rest := args[1:]
	var positional []string
	for {
		if err := fs.Parse(rest); err != nil {
			return parsed, fmt.Errorf("%v\n%s", err, recoveryBundleUsage)
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(positional) != 1 || positional[0] == "" {
		return parsed, fmt.Errorf("give exactly one bundle directory\n%s", recoveryBundleUsage)
	}
	parsed.Dir = positional[0]
	parsed.Force = force
	parsed.ReplaceExisting = replace
	if replace && !force {
		return parsed, fmt.Errorf("--replace-existing only applies with --force\n%s", recoveryBundleUsage)
	}
	if parsed.Action == "write" {
		if len(checkpoints) == 0 {
			return parsed, fmt.Errorf("--checkpoints is required: name the checkpoint root or the archives\n%s", recoveryBundleUsage)
		}
		if (journal == "") == !noJournal {
			return parsed, fmt.Errorf("give --runner-journal <file>, or --no-runner-journal to say the host has none\n%s", recoveryBundleUsage)
		}
		parsed.Host = recoveryBundleHost{Checkpoints: checkpoints, RunnerJournal: journal, NoRunnerJournal: noJournal}
	}
	return parsed, nil
}

// RecoveryBundleCommand runs `report-portal recovery-bundle`. Progress and the summary go to out.
func RecoveryBundleCommand(cfgPath string, args []string, out io.Writer) error {
	parsed, err := parseRecoveryBundleArgs(args)
	if err != nil {
		return err
	}
	// The configuration is loaded, never created: a fresh default would carry a new secret_key, and the keyring
	// in the bundle can only be opened with the one it was sealed under.
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if parsed.Action == "restore" {
		opt := recoveryBundleRestoreOptions{Force: parsed.Force, ReplaceExisting: parsed.ReplaceExisting}
		// The bundle and the secret_key are checked before the target database is touched; a dry run never opens it.
		manifest, err := verifyRecoveryBundleForHost(cfg, parsed.Dir, opt)
		if err != nil {
			return err
		}
		if !parsed.Force {
			report, err := dryRunRecoveryBundle(manifest, parsed.Dir)
			if err != nil {
				return err
			}
			printRecoveryBundleRestored(out, parsed.Dir, report, false)
			return nil
		}
		if cfg.DBDriver != "postgres" {
			if err := os.MkdirAll(config.DirOf(cfg.DBPath), 0o755); err != nil {
				return err
			}
		}
		st, err := OpenStore(cfg.DBDriver, cfg.DBSource())
		if err != nil {
			return err
		}
		defer st.Close()
		s := &Server{st: st, cfg: cfg}
		report, err := s.loadRecoveryBundle(parsed.Dir, manifest, opt)
		if err != nil {
			return err
		}
		printRecoveryBundleRestored(out, parsed.Dir, report, true)
		return nil
	}
	if cfg.DBDriver != "postgres" {
		if err := os.MkdirAll(config.DirOf(cfg.DBPath), 0o755); err != nil {
			return err
		}
	}
	st, err := OpenStore(cfg.DBDriver, cfg.DBSource())
	if err != nil {
		return err
	}
	defer st.Close()
	s := &Server{st: st, cfg: cfg}
	// The directory is created owner-only: the dump carries password and token hashes and the sealed keyring.
	if err := os.MkdirAll(parsed.Dir, 0o700); err != nil {
		return err
	}
	report, err := s.writeRecoveryBundleFromHostReport(parsed.Dir, parsed.Host)
	if err != nil {
		return err
	}
	printRecoveryBundleWritten(out, parsed.Dir, report)
	return nil
}

type recoveryBundleWriteReport struct {
	Manifest platformRecoveryBundleManifest
	// Ignored are archives found in a checkpoint directory that no retained recovery set owns.
	Ignored []string
}

func (s *Server) writeRecoveryBundleFromHost(dir string, host recoveryBundleHost) (platformRecoveryBundleManifest, error) {
	report, err := s.writeRecoveryBundleFromHostReport(dir, host)
	return report.Manifest, err
}

// writeRecoveryBundleFromHostReport resolves the checkpoint archives and the journal from the host paths and writes
// the bundle. Anything it cannot resolve is left out of the map, which the writer refuses.
func (s *Server) writeRecoveryBundleFromHostReport(dir string, host recoveryBundleHost) (recoveryBundleWriteReport, error) {
	var report recoveryBundleWriteReport
	if (host.RunnerJournal == "") == !host.NoRunnerJournal {
		return report, fmt.Errorf("%w: name the runner journal, or state that the host has none", errPlatformBundle)
	}
	sets, err := loadPlatformRetainedSets(s.st.poolExec())
	if err != nil {
		return report, err
	}
	if err := validatePlatformRetainedSets(sets); err != nil {
		return report, err
	}
	archives, ignored, err := resolveRecoveryBundleArchivesReport(sets, host.Checkpoints)
	if err != nil {
		return report, err
	}
	report.Ignored = ignored
	report.Manifest, err = s.writePlatformRecoveryBundle(dir, platformRecoveryBundleInput{Archives: archives, RunnerJournal: host.RunnerJournal})
	return report, err
}

// resolveRecoveryBundleArchives is resolveRecoveryBundleArchivesReport without the list of ignored archives.
func resolveRecoveryBundleArchives(sets []platformRetainedSet, locations []string) (map[string]string, error) {
	archives, _, err := resolveRecoveryBundleArchivesReport(sets, locations)
	return archives, err
}

// resolveRecoveryBundleArchivesReport maps every retained recovery set to its archive: <checkpoint id>.tar, which is
// how the runner names it, in a given directory, in that directory's .portal-checkpoints, or given as a file. A set
// with no archive is left out (the writer refuses it); one with two different archives, or an archive file that no
// retained set owns, is refused. A directory can hold archives of deleted recovery sets, because the runner has no
// command that deletes a checkpoint: those are left out and returned as ignored.
func resolveRecoveryBundleArchivesReport(sets []platformRetainedSet, locations []string) (map[string]string, []string, error) {
	if err := validatePlatformRetainedSets(sets); err != nil {
		return nil, nil, err
	}
	byCheckpoint := map[string]platformRetainedSet{}
	for _, set := range sets {
		byCheckpoint[set.checkpoint] = set
	}
	var dirs []string
	explicit := map[string]string{}
	for _, location := range locations {
		info, err := os.Stat(location)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: cannot read checkpoint location %s", errPlatformBundle, location)
		}
		if info.IsDir() {
			dirs = append(dirs, location)
			continue
		}
		name := filepath.Base(location)
		id := strings.TrimSuffix(name, ".tar")
		if id == name {
			return nil, nil, fmt.Errorf("%w: %s is not named <checkpoint id>.tar", errPlatformBundle, name)
		}
		if _, ok := byCheckpoint[id]; !ok {
			return nil, nil, fmt.Errorf("%w: %s does not belong to a retained recovery set", errPlatformBundle, name)
		}
		if _, dup := explicit[id]; dup {
			return nil, nil, fmt.Errorf("%w: checkpoint %s was given twice", errPlatformBundle, id)
		}
		explicit[id] = location
	}
	archives := map[string]string{}
	used := map[string]bool{}
	for _, set := range sets {
		var found []string
		add := func(path string) {
			info, err := os.Lstat(path)
			if err != nil {
				return
			}
			for _, have := range found {
				if other, err := os.Lstat(have); err == nil && os.SameFile(info, other) {
					return
				}
			}
			found = append(found, path)
		}
		if path, ok := explicit[set.checkpoint]; ok {
			add(path)
		}
		for _, dir := range dirs {
			add(filepath.Join(dir, set.checkpoint+".tar"))
			add(filepath.Join(dir, ".portal-checkpoints", set.checkpoint+".tar"))
		}
		switch len(found) {
		case 0:
		case 1:
			archives[set.id] = found[0]
			used[found[0]] = true
		default:
			return nil, nil, fmt.Errorf("%w: checkpoint %s of recovery set %s is in more than one place: %s", errPlatformBundle, set.checkpoint, set.id, strings.Join(found, ", "))
		}
	}
	var ignored []string
	for _, dir := range dirs {
		for _, root := range []string{dir, filepath.Join(dir, ".portal-checkpoints")} {
			entries, err := os.ReadDir(root)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				path := filepath.Join(root, entry.Name())
				if strings.HasSuffix(entry.Name(), ".tar") && !used[path] {
					if _, owned := byCheckpoint[strings.TrimSuffix(entry.Name(), ".tar")]; !owned {
						ignored = append(ignored, path)
					}
				}
			}
		}
	}
	sort.Strings(ignored)
	return archives, ignored, nil
}

type recoveryBundleRestoreReport struct {
	Manifest platformRecoveryBundleManifest
	Dump     *RestoreReport
	// Paused is true when the restore left the Portal in recovery mode.
	Paused bool
}

type recoveryBundleRestoreOptions struct {
	Force bool
	// ReplaceExisting allows Force over a database that already holds data.
	ReplaceExisting bool

	// Test seams, nil in production. afterVerify runs once the bundle and secret_key are verified; afterBlobs runs
	// after the image blobs were placed and before the dump is loaded.
	afterVerify func()
	afterBlobs  func()
}

// verifyRecoveryBundleForHost checks the bundle's digests and that this host's secret_key opens the bundle's
// keyring. It needs no database: a wrong key or a damaged bundle is found before anything is written, and a dry run
// stops here.
func verifyRecoveryBundleForHost(cfg *config.Config, dir string, opt recoveryBundleRestoreOptions) (platformRecoveryBundleManifest, error) {
	manifest, err := verifyPlatformRecoveryBundle(dir)
	if err != nil {
		return manifest, err
	}
	if err := checkRecoveryBundleKeyring(cfg, dir, manifest); err != nil {
		return manifest, err
	}
	if opt.afterVerify != nil {
		opt.afterVerify()
	}
	return manifest, nil
}

// checkRecoveryBundleKeyring opens the bundle's keyring under the secret_key of this host. It reads and writes
// nothing else: a previous key is deliberately not consulted, because that path would re-wrap the keyring.
func checkRecoveryBundleKeyring(cfg *config.Config, dir string, manifest platformRecoveryBundleManifest) error {
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(manifest.Keyring.Path)))
	if err != nil {
		return err
	}
	var keyring struct {
		Format  string `json:"format"`
		Salt    string `json:"keyring_salt"`
		Wrapped string `json:"keyring_wrapped_dek"`
	}
	if err := json.Unmarshal(raw, &keyring); err != nil || keyring.Format != platformRecoveryBundleFormat+"/keyring" {
		return fmt.Errorf("%w: unreadable keyring", errPlatformBundle)
	}
	probe := &Server{cfg: &config.Config{SecretKey: cfg.SecretKey}}
	dek, err := probe.openStoredKeyring(keyring.Salt, keyring.Wrapped)
	if err != nil {
		return fmt.Errorf("%w: secret_key does not match the bundle: it cannot open the bundle's keyring; use the config.yaml secret_key of the host the bundle came from", errPlatformBundle)
	}
	clear(dek)
	return nil
}

// verifiedDumpReader hashes the dump while it is read and turns the end of the stream into an error if the bytes
// are not the ones the bundle verification hashed. The load reads to the end before it commits, so a dump swapped
// after verification is refused with nothing written.
type verifiedDumpReader struct {
	r    io.Reader
	hash hash.Hash
	n    int64
	want platformBundleFile
}

func newVerifiedDumpReader(r io.Reader, want platformBundleFile) *verifiedDumpReader {
	return &verifiedDumpReader{r: r, hash: sha256.New(), want: want}
}

func (v *verifiedDumpReader) Read(p []byte) (int, error) {
	n, err := v.r.Read(p)
	v.hash.Write(p[:n])
	v.n += int64(n)
	if err == io.EOF && (v.n != v.want.Bytes || hex.EncodeToString(v.hash.Sum(nil)) != v.want.SHA256) {
		return n, fmt.Errorf("%w: the dump is not the one that was verified", errPlatformBundle)
	}
	return n, err
}

// dryRunRecoveryBundle parses the dump against the schema of this build in a scratch in-memory database. The
// target database is not opened, so a dry run creates no directory, file or table.
func dryRunRecoveryBundle(manifest platformRecoveryBundleManifest, dir string) (*recoveryBundleRestoreReport, error) {
	scratch, err := OpenStore("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer scratch.Close()
	dump, err := os.Open(filepath.Join(dir, filepath.FromSlash(manifest.Dump.Path)))
	if err != nil {
		return nil, err
	}
	defer dump.Close()
	rep, err := scratch.restoreFrom(bufio.NewReaderSize(newVerifiedDumpReader(dump, manifest.Dump), 1<<16), false)
	if err != nil {
		return nil, err
	}
	return &recoveryBundleRestoreReport{Manifest: manifest, Dump: rep}, nil
}

// restoreRecoveryBundle verifies the bundle and loads it into this Portal's database.
func (s *Server) restoreRecoveryBundle(dir string, opt recoveryBundleRestoreOptions) (*recoveryBundleRestoreReport, error) {
	manifest, err := verifyRecoveryBundleForHost(s.cfg, dir, opt)
	if err != nil {
		return nil, err
	}
	return s.loadRecoveryBundle(dir, manifest, opt)
}

// loadRecoveryBundle loads a verified bundle. Unless ReplaceExisting is set the database must hold nothing but what a
// new database starts with. The managed image blobs are placed first and stay reserved until the dump has loaded: they
// are content-addressed and harmless on their own, and nothing may sweep them before the rows that name them exist.
func (s *Server) loadRecoveryBundle(dir string, manifest platformRecoveryBundleManifest, opt recoveryBundleRestoreOptions) (*recoveryBundleRestoreReport, error) {
	if !opt.ReplaceExisting {
		if filled, err := nonPristineTables(s.st); err != nil {
			return nil, err
		} else if len(filled) > 0 {
			return nil, fmt.Errorf("the target database already holds data (%s); restore replaces everything in it. "+
				"Stop the Portal and the app runner and pass --replace-existing to replace it, or point config.yaml at an empty database", strings.Join(filled, ", "))
		}
	}
	dump, err := os.Open(filepath.Join(dir, filepath.FromSlash(manifest.Dump.Path)))
	if err != nil {
		return nil, err
	}
	defer dump.Close()
	release, err := s.placePlatformBundleBlobs(dir, manifest)
	if err != nil {
		return nil, err
	}
	defer release()
	if opt.afterBlobs != nil {
		opt.afterBlobs()
	}
	rep, err := s.st.restoreFrom(bufio.NewReaderSize(newVerifiedDumpReader(dump, manifest.Dump), 1<<16), true)
	if err != nil {
		return nil, err
	}
	var paused string
	_ = s.st.queryRow(`SELECT v FROM meta WHERE k='app_platform_restore_paused'`).Scan(&paused)
	return &recoveryBundleRestoreReport{Manifest: manifest, Dump: rep, Paused: paused == "1"}, nil
}

// nonPristineTables lists the tables that hold more rows than a database this build just created.
func nonPristineTables(st *Store) ([]string, error) {
	pristine, err := OpenStore("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer pristine.Close()
	names, err := st.backupTables()
	if err != nil {
		return nil, err
	}
	have, err := st.rowCounts(names)
	if err != nil {
		return nil, err
	}
	base, err := pristine.rowCounts(names)
	if err != nil {
		return nil, err
	}
	var filled []string
	for _, name := range names {
		if have[name] > base[name] {
			filled = append(filled, name)
		}
	}
	return filled, nil
}

func printRecoveryBundleWritten(out io.Writer, dir string, r recoveryBundleWriteReport) {
	m := r.Manifest
	fmt.Fprintf(out, "recovery bundle written to %s\n", dir)
	fmt.Fprintf(out, "  %-22s %d bytes\n", m.Dump.Path, m.Dump.Bytes)
	fmt.Fprintf(out, "  %-22s %d bytes\n", m.Keyring.Path, m.Keyring.Bytes)
	for _, c := range m.Checkpoints {
		fmt.Fprintf(out, "  %s  recovery set %s  sha256 %s\n", c.Archive.Path, c.RecoverySetID, c.Archive.SHA256)
	}
	if m.RunnerJournal != nil {
		fmt.Fprintf(out, "  %s (provenance only)\n", m.RunnerJournal.Path)
	}
	fmt.Fprintf(out, "  %d checkpoint archive(s), %d required release(s), %d image blob(s)\n", len(m.Checkpoints), len(m.Releases), len(m.ImageBlobs))
	for _, path := range r.Ignored {
		fmt.Fprintf(out, "  ignored (no retained recovery set owns it): %s\n", path)
	}
	fmt.Fprintln(out, "The bundle does not contain config.yaml: carry the original secret_key separately, or the keyring cannot be opened.")
	fmt.Fprintln(out, "The directory holds password and token hashes; keep it owner-only and move it over a trusted channel.")
}

func printRecoveryBundleRestored(out io.Writer, dir string, r *recoveryBundleRestoreReport, force bool) {
	files := 2 + len(r.Manifest.Checkpoints) + len(r.Manifest.ImageBlobs)
	if r.Manifest.RunnerJournal != nil {
		files++
	}
	fmt.Fprintf(out, "bundle %s verified: %d file digest(s) match, and secret_key opens the bundle keyring\n", dir, files)
	fmt.Fprintf(out, "dump written %s by %s (%s): %d rows in %d tables\n", r.Dump.Header.CreatedAt, r.Dump.Header.AppVersion, r.Dump.Header.Driver, r.Dump.Total, len(r.Dump.Rows))
	if !force {
		fmt.Fprintln(out, "DRY RUN: the dump parses against this build's schema. The target database was not opened and nothing was written. Re-run with --force to load it.")
		return
	}
	fmt.Fprintf(out, "restore complete; %d existing rows were replaced; %d image blob(s) placed\n", r.Dump.Replaced, len(r.Manifest.ImageBlobs))
	if r.Dump.Replaced > 0 {
		fmt.Fprintln(out, "WARNING: an existing database was replaced. This command cannot tell whether a Portal or runner is still using it; make sure both were stopped.")
	}
	if r.Paused {
		fmt.Fprintln(out, "The Portal is in recovery mode: every runtime binding is retired and tasks and subscriptions await review.")
	} else {
		fmt.Fprintln(out, "The dump held no app platform entities, so the Portal was not paused.")
	}
	fmt.Fprintln(out, "Next, on the runner host, provide these checkpoint archives from the bundle for the import review (doc 168):")
	for _, c := range r.Manifest.Checkpoints {
		fmt.Fprintf(out, "  %s  recovery set %s  sha256 %s\n", filepath.Join(dir, filepath.FromSlash(c.Archive.Path)), c.RecoverySetID, c.Archive.SHA256)
	}
	if r.Manifest.RunnerJournal != nil {
		fmt.Fprintf(out, "The old runner journal is provenance only and was not opened: %s\n", filepath.Join(dir, filepath.FromSlash(r.Manifest.RunnerJournal.Path)))
	}
}

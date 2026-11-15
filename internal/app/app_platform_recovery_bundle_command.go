package app

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
  report-portal recovery-bundle restore <dir> [--force]   (without --force it is a dry run)`

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
	var noJournal, force bool
	if parsed.Action == "write" {
		fs.Var(&checkpoints, "checkpoints", "")
		fs.StringVar(&journal, "runner-journal", "", "")
		fs.BoolVar(&noJournal, "no-runner-journal", false, "")
	} else {
		fs.BoolVar(&force, "force", false, "")
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
	if parsed.Action == "write" {
		// The directory is created owner-only: the dump carries password and token hashes and the sealed keyring.
		if err := os.MkdirAll(parsed.Dir, 0o700); err != nil {
			return err
		}
		manifest, err := s.writeRecoveryBundleFromHost(parsed.Dir, parsed.Host)
		if err != nil {
			return err
		}
		printRecoveryBundleWritten(out, parsed.Dir, manifest)
		return nil
	}
	report, err := s.restoreRecoveryBundle(parsed.Dir, parsed.Force)
	if err != nil {
		return err
	}
	printRecoveryBundleRestored(out, parsed.Dir, report, parsed.Force)
	return nil
}

// writeRecoveryBundleFromHost resolves the checkpoint archives and the journal from the host paths and writes the
// bundle. Anything it cannot resolve is left out of the map, which the writer refuses.
func (s *Server) writeRecoveryBundleFromHost(dir string, host recoveryBundleHost) (platformRecoveryBundleManifest, error) {
	var zero platformRecoveryBundleManifest
	if (host.RunnerJournal == "") == !host.NoRunnerJournal {
		return zero, fmt.Errorf("%w: name the runner journal, or state that the host has none", errPlatformBundle)
	}
	sets, err := loadPlatformRetainedSets(s.st.poolExec())
	if err != nil {
		return zero, err
	}
	archives, err := resolveRecoveryBundleArchives(sets, host.Checkpoints)
	if err != nil {
		return zero, err
	}
	return s.writePlatformRecoveryBundle(dir, platformRecoveryBundleInput{Archives: archives, RunnerJournal: host.RunnerJournal})
}

// resolveRecoveryBundleArchives maps every retained recovery set to its archive: <checkpoint id>.tar, which is
// how the runner names it, in a given directory, in that directory's .portal-checkpoints, or given as a file. A set
// with no archive is left out (the writer refuses it); one with two different archives, or an archive no retained
// set owns, is refused here.
func resolveRecoveryBundleArchives(sets []platformRetainedSet, locations []string) (map[string]string, error) {
	byCheckpoint := map[string]platformRetainedSet{}
	for _, set := range sets {
		byCheckpoint[set.checkpoint] = set
	}
	var dirs []string
	explicit := map[string]string{}
	for _, location := range locations {
		info, err := os.Stat(location)
		if err != nil {
			return nil, fmt.Errorf("%w: cannot read checkpoint location %s", errPlatformBundle, location)
		}
		if info.IsDir() {
			dirs = append(dirs, location)
			continue
		}
		name := filepath.Base(location)
		id := strings.TrimSuffix(name, ".tar")
		if id == name {
			return nil, fmt.Errorf("%w: %s is not named <checkpoint id>.tar", errPlatformBundle, name)
		}
		if _, ok := byCheckpoint[id]; !ok {
			return nil, fmt.Errorf("%w: %s does not belong to a retained recovery set", errPlatformBundle, name)
		}
		if _, dup := explicit[id]; dup {
			return nil, fmt.Errorf("%w: checkpoint %s was given twice", errPlatformBundle, id)
		}
		explicit[id] = location
	}
	archives := map[string]string{}
	for _, set := range sets {
		var found []string
		add := func(path string) error {
			info, err := os.Lstat(path)
			if err != nil {
				return nil
			}
			for _, have := range found {
				if other, err := os.Lstat(have); err == nil && os.SameFile(info, other) {
					return nil
				}
			}
			found = append(found, path)
			return nil
		}
		if path, ok := explicit[set.checkpoint]; ok {
			_ = add(path)
		}
		for _, dir := range dirs {
			_ = add(filepath.Join(dir, set.checkpoint+".tar"))
			_ = add(filepath.Join(dir, ".portal-checkpoints", set.checkpoint+".tar"))
		}
		switch len(found) {
		case 0:
		case 1:
			archives[set.id] = found[0]
		default:
			return nil, fmt.Errorf("%w: checkpoint %s of recovery set %s is in more than one place: %s", errPlatformBundle, set.checkpoint, set.id, strings.Join(found, ", "))
		}
	}
	return archives, nil
}

type recoveryBundleRestoreReport struct {
	Manifest platformRecoveryBundleManifest
	Dump     *RestoreReport
}

// restoreRecoveryBundle verifies the bundle and, with force, loads it into this Portal's database. Without force
// it verifies the bundle and parses the dump and writes nothing, like `restore`. The managed image blobs are
// placed first: they are content-addressed and harmless on their own, so a failure there leaves the database
// untouched.
func (s *Server) restoreRecoveryBundle(dir string, force bool) (*recoveryBundleRestoreReport, error) {
	manifest, err := verifyPlatformRecoveryBundle(dir)
	if err != nil {
		return nil, err
	}
	dump, err := os.Open(filepath.Join(dir, filepath.FromSlash(manifest.Dump.Path)))
	if err != nil {
		return nil, err
	}
	defer dump.Close()
	if force {
		if err := s.restorePlatformBundleBlobs(dir, manifest); err != nil {
			return nil, err
		}
	}
	rep, err := s.st.restoreFrom(bufio.NewReaderSize(dump, 1<<16), force)
	if err != nil {
		return nil, err
	}
	if force {
		// The keyring in the bundle is the one the dump carried; a restored database that holds another could
		// not open the credentials the bundle's recovery sets refer to.
		want, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(manifest.Keyring.Path)))
		if err != nil {
			return nil, err
		}
		have, err := platformBundleKeyring(s.st.poolExec())
		if err != nil || string(have) != string(want) {
			return nil, fmt.Errorf("%w: the restored keyring is not the one in the bundle (the database was loaded; do not start the Portal)", errPlatformBundle)
		}
	}
	return &recoveryBundleRestoreReport{Manifest: manifest, Dump: rep}, nil
}

func printRecoveryBundleWritten(out io.Writer, dir string, m platformRecoveryBundleManifest) {
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
	fmt.Fprintln(out, "The bundle does not contain config.yaml: carry the original secret_key separately, or the keyring cannot be opened.")
	fmt.Fprintln(out, "The directory holds password and token hashes; keep it owner-only and move it over a trusted channel.")
}

func printRecoveryBundleRestored(out io.Writer, dir string, r *recoveryBundleRestoreReport, force bool) {
	fmt.Fprintf(out, "bundle %s verified: %d file digest(s) match\n", dir, 2+len(r.Manifest.Checkpoints)+len(r.Manifest.ImageBlobs)+boolInt(r.Manifest.RunnerJournal != nil))
	fmt.Fprintf(out, "dump written %s by %s (%s): %d rows in %d tables\n", r.Dump.Header.CreatedAt, r.Dump.Header.AppVersion, r.Dump.Header.Driver, r.Dump.Total, len(r.Dump.Rows))
	if !force {
		fmt.Fprintf(out, "DRY RUN: nothing was written. A restore would DELETE %d existing rows. Re-run with --force to apply, after stopping the portal.\n", r.Dump.Replaced)
		return
	}
	fmt.Fprintf(out, "restore complete; %d existing rows were replaced; %d image blob(s) placed\n", r.Dump.Replaced, len(r.Manifest.ImageBlobs))
	fmt.Fprintln(out, "The Portal is in recovery mode: every runtime binding is retired and tasks and subscriptions await review.")
	fmt.Fprintln(out, "Next, on the runner host, provide these checkpoint archives from the bundle for the import review (doc 168):")
	for _, c := range r.Manifest.Checkpoints {
		fmt.Fprintf(out, "  %s  recovery set %s  sha256 %s\n", filepath.Join(dir, filepath.FromSlash(c.Archive.Path)), c.RecoverySetID, c.Archive.SHA256)
	}
	if r.Manifest.RunnerJournal != nil {
		fmt.Fprintf(out, "The old runner journal is provenance only and was not opened: %s\n", filepath.Join(dir, filepath.FromSlash(r.Manifest.RunnerJournal.Path)))
	}
}

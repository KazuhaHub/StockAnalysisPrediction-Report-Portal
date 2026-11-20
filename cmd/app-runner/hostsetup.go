package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/hostsetup"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/provision"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/version"
)

const hostSetupUsage = `usage: app-runner host-setup [--apply] [--enrollment FILE] [flags]

Prepares this host for the compose runner, or adopts the layout a systemd runner already uses. Without --apply it
prints the plan and changes nothing (and takes no lock, so it is safe while the runner serves). With --apply it
creates what is missing and verifies the rest; running it again changes nothing and says so. Run it as root in
the host namespaces: docker compose run --rm app-runner host-setup

  --apply                          make the changes the plan lists
  --enrollment FILE                the public enrollment document prepared in the Portal console (a host path).
                                   Without it a new host is prepared but gets no configuration and exit status 3
  --config-dir DIR                 service.json and bootstrap.json (default /etc/portal-app-runner)
  --release TAG                    the tag printed as PORTAL_VERSION in the .env report

Layout flags. A new host takes the defaults; an adopted host keeps its service.json, and a flag that is given
and disagrees with it refuses.
  --controller-uid N               default 41001
  --controller-gid N               default 41001
  --docker-socket PATH             default /run/docker.sock
  --journal-dir, --image-root, --control-root, --ipc-root PATH
  --data-root PATH, --checkpoint-root PATH   new host: <mount point>/root (default /srv/portal-runner/data/root, /srv/portal-runner/checkpoint/root)

New host only:
  --image-dir DIR                  where data.img and checkpoint.img go (default /var/lib/portal-app-runner/volumes)
  --data-size SIZE                 default 8G
  --checkpoint-size SIZE           default 16G; below 16G it refuses unless the next flag is given
  --checkpoint-size-below-production

Exit status: 0 done or nothing to do, 1 refused or failed, 2 usage, 3 enrollment pending.
See docs/app-platform/187-compose-app-runner.md section 8.
`

func parseID(name, value string) (uint32, error) {
	n, err := strconv.ParseUint(value, 10, 32)
	if err != nil || n == 0 || n == 1<<32-1 {
		return 0, fmt.Errorf("%s must be a number from 1 to 4294967294", name)
	}
	return uint32(n), nil
}

// hostSetup is the operator command of doc 187 section 8. The environment is a parameter so tests never touch
// the host.
func hostSetup(ctx context.Context, args []string, out io.Writer, system func() (hostsetup.Env, error)) int {
	flags := flag.NewFlagSet("host-setup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var opts hostsetup.Options
	var uid, gid, dataSize, checkpointSize string
	flags.BoolVar(&opts.Apply, "apply", false, "")
	flags.StringVar(&opts.ConfigDir, "config-dir", "", "")
	flags.StringVar(&opts.Enrollment, "enrollment", "", "")
	flags.StringVar(&opts.Release, "release", "", "")
	flags.BoolVar(&opts.BelowProduction, "checkpoint-size-below-production", false, "")
	flags.StringVar(&uid, "controller-uid", "", "")
	flags.StringVar(&gid, "controller-gid", "", "")
	flags.StringVar(&opts.DockerSocket, "docker-socket", "", "")
	flags.StringVar(&opts.JournalDir, "journal-dir", "", "")
	flags.StringVar(&opts.ImageRoot, "image-root", "", "")
	flags.StringVar(&opts.IPCRoot, "ipc-root", "", "")
	flags.StringVar(&opts.ControlRoot, "control-root", "", "")
	flags.StringVar(&opts.DataRoot, "data-root", "", "")
	flags.StringVar(&opts.CheckpointRoot, "checkpoint-root", "", "")
	flags.StringVar(&opts.ImageDir, "image-dir", "", "")
	flags.StringVar(&dataSize, "data-size", "", "")
	flags.StringVar(&checkpointSize, "checkpoint-size", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprint(out, hostSetupUsage)
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	usage := func(err error) int {
		fmt.Fprintln(out, err)
		fmt.Fprint(out, hostSetupUsage)
		return 2
	}
	var err error
	if uid != "" {
		if opts.ControllerUID, err = parseID("--controller-uid", uid); err != nil {
			return usage(err)
		}
	}
	if gid != "" {
		if opts.ControllerGID, err = parseID("--controller-gid", gid); err != nil {
			return usage(err)
		}
	}
	if dataSize != "" {
		if opts.DataSize, err = provision.ParseSize(dataSize); err != nil {
			return usage(err)
		}
	}
	if checkpointSize != "" {
		if opts.CheckpointSize, err = provision.ParseSize(checkpointSize); err != nil {
			return usage(err)
		}
	}
	if opts.Release == "" && version.IsReleaseTag(version.Version) {
		opts.Release = version.Version
	}
	env, err := system()
	if err != nil {
		fmt.Fprintln(out, err)
		return 1
	}
	return hostsetup.Run(ctx, env, opts, out)
}

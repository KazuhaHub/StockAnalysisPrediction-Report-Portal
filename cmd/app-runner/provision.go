package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/provision"
)

const provisionUsage = `usage: app-runner provision-xfs-images [--dry-run]
    --data-image PATH --data-size SIZE --checkpoint-image PATH --checkpoint-size SIZE
    --mount-data DIR --mount-checkpoint DIR

Creates two preallocated, loop-mounted XFS images with project quota, for a host
whose own filesystem (for example ext4) has none. Run once, as root.
SIZE is binary: 8G is 8 GiB (minimum 1G). It changes nothing under /etc, refuses
paths that already hold other content and never touches a mount it did not
create. Re-running after success only re-verifies. --dry-run reports the plan.
See docs/app-platform/07-handbook.md and docs/app-platform/182-cross-platform-backend-runtime.md.
`

// provisionXFSImages is the operator command behind doc 182 section 3.2. The environment is a parameter so tests
// never touch the host.
func provisionXFSImages(ctx context.Context, args []string, out io.Writer, system func() (provision.Env, error)) int {
	flags := flag.NewFlagSet("provision-xfs-images", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var spec provision.Spec
	var dataSize, checkpointSize string
	dryRun := flags.Bool("dry-run", false, "report what would be done and change nothing")
	flags.StringVar(&spec.DataImage, "data-image", "", "")
	flags.StringVar(&dataSize, "data-size", "", "")
	flags.StringVar(&spec.CheckpointImage, "checkpoint-image", "", "")
	flags.StringVar(&checkpointSize, "checkpoint-size", "", "")
	flags.StringVar(&spec.DataMount, "mount-data", "", "")
	flags.StringVar(&spec.CheckpointMount, "mount-checkpoint", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(out, provisionUsage)
			return 0
		}
		fmt.Fprint(out, provisionUsage)
		return 2
	}
	var err error
	if spec.DataSize, err = provision.ParseSize(dataSize); err != nil {
		fmt.Fprintln(out, err)
		fmt.Fprint(out, provisionUsage)
		return 2
	}
	if spec.CheckpointSize, err = provision.ParseSize(checkpointSize); err != nil {
		fmt.Fprintln(out, err)
		fmt.Fprint(out, provisionUsage)
		return 2
	}
	if err := spec.Validate(); err != nil {
		fmt.Fprintln(out, err)
		return 2
	}
	env, err := system()
	if err == nil {
		err = provision.Run(ctx, env, spec, *dryRun, out)
	}
	switch {
	case err == nil:
		return 0
	case errors.Is(err, provision.ErrInvalid):
		fmt.Fprintln(out, err)
		return 2
	default:
		fmt.Fprintln(out, err)
		return 1
	}
}

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/packimport"
)

const packerVersion = "app-runner/1"

const packImportUsage = `usage: app-runner pack-import --from DIRECTORY --out CHECKPOINT_ROOT/.portal-imports/NAME.tar

Packs a prepared directory tree into an import archive for the administrator
data import (doc 185 section 5). Run as root on the runner host.
The tree may hold only regular files and directories: symbolic links, special
files, setuid, setgid and sticky bits, the reserved name
.portal-checkpoint-manifest.json, names over 255 bytes and trees deeper than 64
levels are refused. Hard links are kept when every link of the file is inside the
tree (stored once, recreated as links on import); a file with a link outside the
tree is refused. Owners and
modes are normalised to 65532:65532, 0750 for directories and 0640 for files.
The archive is written into the import inbox (root-owned, mode 0700) and an
existing archive is never overwritten. It prints the SHA-256, size and entry
count; copy the SHA-256 into the Portal import review.
See docs/app-platform/07-handbook.md, section "Importing data".
`

// packImport is the operator command behind doc 185 section 5.1. The packer is a parameter so tests never need root.
func packImport(ctx context.Context, args []string, out io.Writer, euid int, pack func(context.Context, packimport.Options) (packimport.Result, error)) int {
	flags := flag.NewFlagSet("pack-import", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	from := flags.String("from", "", "")
	target := flags.String("out", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *from == "" || *target == "" {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(out, packImportUsage)
			return 0
		}
		fmt.Fprint(out, packImportUsage)
		return 2
	}
	if euid != 0 {
		fmt.Fprintln(out, "pack-import must run as root: it reads the tree as root and writes a root-owned archive")
		return 1
	}
	result, err := pack(ctx, packimport.Options{From: *from, Out: *target, Packer: packerVersion})
	if err != nil {
		fmt.Fprintln(out, "pack-import refused:", err)
		return 1
	}
	fmt.Fprintf(out, "sha256:  %s\nbytes:   %d\nentries: %d\n", result.SHA256, result.Bytes, result.Entries)
	return 0
}

func runPackImport(ctx context.Context, args []string, out io.Writer) int {
	return packImport(ctx, args, out, os.Geteuid(), packimport.Pack)
}

// app-runner currently exposes read-only daemon inspection, not a running
// workload service or an automatic controller enrollment path.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func run(ctx context.Context, args []string, out io.Writer) int {
	if len(args) == 0 || args[0] != "inspect-daemon" {
		fmt.Fprintln(out, "usage: app-runner inspect-daemon --socket /run/docker.sock")
		return 2
	}
	flags := flag.NewFlagSet("inspect-daemon", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socket := flags.String("socket", "", "explicit canonical Docker Unix socket")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || *socket == "" {
		fmt.Fprintln(out, "an explicit --socket is required")
		return 2
	}
	report, err := driver.InspectDaemon(ctx, *socket)
	code := ""
	if err != nil {
		code = "inspection_failed"
		if err == driver.ErrUnsupported {
			code = "host_profile_unsupported"
		} else if err == driver.ErrUnsafeSocket {
			code = "unsafe_socket"
		} else if err == driver.ErrAccessDenied {
			code = "socket_access_denied"
		}
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if encodeErr := encoder.Encode(struct {
		Report driver.Report `json:"report"`
		Error  string        `json:"error,omitempty"`
	}{report, code}); encodeErr != nil {
		return 1
	}
	if err != nil {
		return 1
	}
	// Zero means metadata inspection completed. report.execution_ready remains
	// false and is never an install/execute authorization.
	return 0
}

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout)) }

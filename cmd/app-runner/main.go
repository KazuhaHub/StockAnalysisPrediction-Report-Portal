// app-runner exposes read-only inspection plus an explicitly configured runtime
// service. Bootstrap is separate from serve and never happens automatically.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/service"
)

func run(ctx context.Context, args []string, out io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(out, "usage: app-runner <inspect-daemon|bootstrap|serve>")
		return 2
	}
	switch args[0] {
	case "inspect-daemon":
		return inspectDaemon(ctx, args[1:], out)
	case "bootstrap":
		return bootstrap(ctx, args[1:], out)
	case "serve":
		return serve(ctx, args[1:], out)
	default:
		fmt.Fprintln(out, "usage: app-runner <inspect-daemon|bootstrap|serve>")
		return 2
	}
}

func inspectDaemon(ctx context.Context, args []string, out io.Writer) int {
	flags := flag.NewFlagSet("inspect-daemon", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socket := flags.String("socket", "", "explicit canonical Docker Unix socket")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *socket == "" {
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

func bootstrap(ctx context.Context, args []string, out io.Writer) int {
	flags := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "explicit private bootstrap configuration")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *path == "" {
		fmt.Fprintln(out, "an explicit --config is required")
		return 2
	}
	cfg, err := service.LoadBootstrapConfig(*path)
	if err != nil {
		fmt.Fprintln(out, "runner bootstrap configuration is invalid")
		return 1
	}
	result, err := service.Bootstrap(ctx, cfg)
	if err != nil {
		fmt.Fprintln(out, "runner bootstrap failed")
		return 1
	}
	_ = json.NewEncoder(out).Encode(map[string]any{
		"created": result.Created, "deployment_id": result.Deployment.DeploymentID,
		"controller_epoch": result.Deployment.ControllerEpoch,
		"control_revision": result.Deployment.ControlRevision,
		"fingerprint":      result.Deployment.Fingerprint,
	})
	return 0
}

func serve(ctx context.Context, args []string, out io.Writer) int {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "explicit private service configuration")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *path == "" {
		fmt.Fprintln(out, "an explicit --config is required")
		return 2
	}
	logger := log.New(out, "", log.LstdFlags)
	if err := service.Serve(ctx, *path, logger); err != nil {
		switch {
		case errors.Is(err, service.ErrConfig):
			fmt.Fprintln(out, "runner service configuration is invalid")
		case errors.Is(err, service.ErrUnsupported):
			fmt.Fprintln(out, "runner service is unsupported on this host")
		default:
			fmt.Fprintln(out, "runner service is not ready")
		}
		return 1
	}
	return 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), serviceSignals()...)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout))
}

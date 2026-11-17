package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/app"
)

func run(ctx context.Context, args []string, out io.Writer) int {
	flags := flag.NewFlagSet("app-task-worker", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", os.Getenv("RP_CONFIG"), "Portal configuration file")
	workerID := flags.String("worker-id", "", "Stable worker UUID")
	interval := flags.Duration("interval", time.Second, "Idle polling interval")
	once := flags.Bool("once", false, "Claim and execute at most one task")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *configPath == "" {
		fmt.Fprintln(out, "usage: app-task-worker --config <file> [--worker-id <uuid>] [--interval 1s] [--once]")
		return 2
	}
	err := app.RunPlatformTaskWorker(ctx, *configPath, app.PlatformTaskWorkerOptions{
		WorkerID: *workerID, Interval: *interval, Once: *once,
	})
	if err != nil {
		log.Printf("application task worker: %v", err)
		return 1
	}
	return 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if code := run(ctx, os.Args[1:], os.Stdout); code != 0 {
		os.Exit(code)
	}
}

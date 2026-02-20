package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"alerting/internal/app"
	"alerting/internal/clock"
	"alerting/internal/config"
)

// main starts alerting service using file or directory config source.
// Params: CLI flags (--config-file or --config-dir).
// Returns: process exit code by startup/run result.
func main() {
	var (
		configFile = flag.String("config-file", "", "path to one TOML config file")
		configDir  = flag.String("config-dir", "", "path to directory with TOML config fragments")
	)
	flag.Parse()

	source, err := config.FromCLI(*configFile, *configDir)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(2)
	}

	service, err := app.NewService(source, clock.RealClock{})
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "service init failed:", err.Error())
		os.Exit(1)
	}

	if err := service.Run(context.Background()); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "service run failed:", err.Error())
		os.Exit(1)
	}
}

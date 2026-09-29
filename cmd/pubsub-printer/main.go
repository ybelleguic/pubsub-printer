package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/ybelleguic/pubsub-printer/internal/app"
	"github.com/ybelleguic/pubsub-printer/internal/config"
	"github.com/ybelleguic/pubsub-printer/internal/version"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	cfg, err := config.Load(os.Args[1:], os.Environ())
	if err != nil {
		var help *config.HelpError
		if errors.As(err, &help) {
			fmt.Fprint(os.Stdout, help.Usage)
			os.Exit(0)
		}
		if errors.Is(err, config.ErrVersion) {
			fmt.Println("pubsub-printer " + version.Version)
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "pubsub-printer: %v\n", err)
		os.Exit(2)
	}
	if err := app.Run(context.Background(), cfg, os.Stdout, logger); err != nil {
		logger.Error("stopped", "err", err)
		os.Exit(1)
	}
}

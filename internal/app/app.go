// Package app runs the Pub/Sub consumer until the context is cancelled.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/pubsub/v2"
	"github.com/ybelleguic/pubsub-printer/internal/config"
	"github.com/ybelleguic/pubsub-printer/internal/gcp"
	"github.com/ybelleguic/pubsub-printer/internal/health"
	"github.com/ybelleguic/pubsub-printer/internal/printer"
)

// Run consumes the configured subscription until SIGINT, SIGTERM, or ctx is done.
// Messages are written to stdout. Logs go to logger, which defaults to stderr JSON.
func Run(ctx context.Context, cfg config.Config, stdout io.Writer, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return run(ctx, cfg, stdout, logger)
}

func run(ctx context.Context, cfg config.Config, stdout io.Writer, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	if stdout == nil {
		stdout = os.Stdout
	}
	if err := publishRuntimeEnv(cfg); err != nil {
		return err
	}
	if cfg.CredentialsFile != "" && cfg.EmulatorHost == "" {
		if _, err := os.Stat(cfg.CredentialsFile); err != nil {
			return fmt.Errorf("credentials file: %w", err)
		}
	}

	subName, err := config.SubscriptionResource(cfg.ProjectID, cfg.Subscription)
	if err != nil {
		return err
	}
	if cfg.UniverseDomain == "s3nsapis.fr" && !strings.Contains(cfg.ProjectID, ":") {
		logger.Warn("S3NS project ids usually include the universe prefix", "example", "s3ns:"+cfg.ProjectID)
	}
	logger.Info("consuming subscription",
		"project", cfg.ProjectID,
		"subscription", subName,
		"universe", gcp.EffectiveUniverse(cfg),
		"endpoint", gcp.EffectiveEndpoint(cfg),
		"auth", gcp.AuthMode(cfg),
		"ack", cfg.Ack,
		"format", cfg.Format,
	)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var state health.State
	var healthErr chan error
	if cfg.HealthAddr != "" {
		healthErr = make(chan error, 1)
		srv := health.New(cfg.HealthAddr, &state)
		go func() {
			healthErr <- srv.Run(ctx)
		}()
		logger.Info("health server", "addr", cfg.HealthAddr)
	}

	recvErr := make(chan error, 1)
	go func() {
		recvErr <- receive(ctx, cfg, subName, stdout, logger, &state)
	}()

	select {
	case err := <-recvErr:
		cancel()
		if healthErr != nil {
			if herr := <-healthErr; herr != nil && err == nil {
				err = herr
			}
		}
		return err
	case err := <-healthErr:
		// A nil health error means the parent context was cancelled (SIGINT,
		// SIGTERM, or Cloud Run shutdown). Wait for the pull loop and return
		// its result so a graceful stop stays a zero exit.
		cancel()
		recv := <-recvErr
		if err != nil {
			return err
		}
		return recv
	}
}

func publishRuntimeEnv(cfg config.Config) error {
	if cfg.EmulatorHost != "" {
		// The v2 client skips its gRPC connection pool when this variable is set.
		// The actual emulator dial options are still passed explicitly.
		return os.Setenv("PUBSUB_EMULATOR_HOST", cfg.EmulatorHost)
	}
	if cfg.UniverseDomain != "" {
		// Client libraries and Application Default Credentials both read this
		// variable. It must agree with option.WithUniverseDomain, including on
		// GKE Workload Identity and Cloud Run where the metadata server reports
		// the same universe.
		return os.Setenv("GOOGLE_CLOUD_UNIVERSE_DOMAIN", cfg.UniverseDomain)
	}
	return nil
}

func receive(ctx context.Context, cfg config.Config, subName string, stdout io.Writer, logger *slog.Logger, state *health.State) error {
	opts, err := gcp.ClientOptions(cfg)
	if err != nil {
		return err
	}
	client, err := pubsub.NewClient(ctx, cfg.ProjectID, opts...)
	if err != nil {
		err = gcp.UniverseHint(err)
		state.SetError(err)
		return err
	}
	defer func() {
		if err := client.Close(); err != nil {
			logger.Error("close pubsub client", "err", err)
		}
	}()

	sub := client.Subscriber(cfg.Subscription)
	sub.ReceiveSettings.MaxOutstandingMessages = cfg.MaxOutstandingMessages
	sub.ReceiveSettings.MaxOutstandingBytes = cfg.MaxOutstandingBytes
	sub.ReceiveSettings.NumGoroutines = cfg.NumGoroutines

	out := printer.New(stdout, cfg.Format, cfg.Color, cfg.MaxBodyBytes)
	state.SetReady(true)
	defer state.SetReady(false)

	err = sub.Receive(ctx, func(cbCtx context.Context, msg *pubsub.Message) {
		rec := printer.Record{
			Project:      cfg.ProjectID,
			Subscription: subName,
			Universe:     gcp.EffectiveUniverse(cfg),
			Endpoint:     gcp.EffectiveEndpoint(cfg),
			ID:           msg.ID,
			Data:         msg.Data,
			Attributes:   msg.Attributes,
			PublishTime:  msg.PublishTime,
			ReceivedAt:   time.Now().UTC(),
			OrderingKey:  msg.OrderingKey,
			Attempt:      msg.DeliveryAttempt,
		}
		if err := out.Print(rec); err != nil {
			logger.Error("write message", "message_id", msg.ID, "err", err)
			msg.Nack()
			return
		}
		if !cfg.Ack {
			msg.Nack()
			return
		}
		if cfg.ExactlyOnce {
			status, ackErr := msg.AckWithResult().Get(cbCtx)
			if ackErr != nil || status != pubsub.AcknowledgeStatusSuccess {
				logger.Error("ack", "message_id", msg.ID, "status", status, "err", ackErr)
			}
			return
		}
		msg.Ack()
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		state.SetError(err)
		return fmt.Errorf("receive: %w", err)
	}
	return nil
}

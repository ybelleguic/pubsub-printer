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
	if cfg.CredentialsFile != "" {
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

	return receive(ctx, cfg, subName, stdout, logger)
}

func publishRuntimeEnv(cfg config.Config) error {
	if cfg.UniverseDomain != "" {
		// Client libraries and Application Default Credentials both read this
		// variable. It must agree with option.WithUniverseDomain, including on
		// GKE Workload Identity and Cloud Run where the metadata server reports
		// the same universe.
		return os.Setenv("GOOGLE_CLOUD_UNIVERSE_DOMAIN", cfg.UniverseDomain)
	}
	return nil
}

func receive(ctx context.Context, cfg config.Config, subName string, stdout io.Writer, logger *slog.Logger) error {
	client, err := pubsub.NewClient(ctx, cfg.ProjectID, gcp.ClientOptions(cfg)...)
	if err != nil {
		return gcp.UniverseHint(err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			logger.Error("close pubsub client", "err", err)
		}
	}()

	sub := client.Subscriber(cfg.Subscription)
	sub.ReceiveSettings.MaxOutstandingMessages = cfg.MaxOutstandingMessages
	sub.ReceiveSettings.MaxOutstandingBytes = cfg.MaxOutstandingBytes

	out := printer.New(stdout, cfg.Format, cfg.MaxBodyBytes)

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
		return fmt.Errorf("receive: %w", err)
	}
	return nil
}

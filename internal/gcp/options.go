// Package gcp builds Google Cloud client options for a standard universe
// (googleapis.com) or a partner universe such as s3nsapis.fr.
package gcp

import (
	"fmt"

	"github.com/ybelleguic/pubsub-printer/internal/config"
	"github.com/ybelleguic/pubsub-printer/internal/version"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const defaultUniverse = "googleapis.com"

// Plan is the resolved dial plan. It is separate from the opaque client
// options so tests can check universe and endpoint selection directly.
type Plan struct {
	UniverseDomain  string
	Endpoint        string
	CredentialsFile string
	QuotaProject    string
	Emulator        bool
}

// BuildPlan decides how the Pub/Sub client authenticates and which API host it uses.
//
// On GKE Workload Identity and Cloud Run, leave credentials empty. The client
// libraries then use Application Default Credentials and the metadata server.
// For a non-default universe, UniverseDomain must match the credentials
// (GOOGLE_CLOUD_UNIVERSE_DOMAIN, for example s3nsapis.fr).
func BuildPlan(cfg config.Config) (Plan, error) {
	if cfg.EmulatorHost != "" {
		return Plan{Emulator: true, Endpoint: cfg.EmulatorHost}, nil
	}
	endpoint, err := ResolveEndpoint(cfg.UniverseDomain, cfg.Endpoint)
	if err != nil {
		return Plan{}, err
	}
	return Plan{
		UniverseDomain:  cfg.UniverseDomain,
		Endpoint:        endpoint,
		CredentialsFile: cfg.CredentialsFile,
		QuotaProject:    cfg.QuotaProject,
	}, nil
}

// ResolveEndpoint returns the Pub/Sub gRPC target.
// An explicit endpoint wins. Otherwise a non-default universe uses
// pubsub.<universe>:443, which is how s3nsapis.fr exposes pubsub.googleapis.com.
// The default universe keeps an empty endpoint so the client library can apply
// its own endpoint template.
func ResolveEndpoint(universe, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if universe == "" || universe == defaultUniverse {
		return "", nil
	}
	return "pubsub." + universe + ":443", nil
}

// EffectiveEndpoint is the host shown in logs and message metadata.
func EffectiveEndpoint(cfg config.Config) string {
	plan, err := BuildPlan(cfg)
	if err != nil {
		return ""
	}
	if plan.Endpoint != "" {
		return plan.Endpoint
	}
	universe := cfg.UniverseDomain
	if universe == "" {
		universe = defaultUniverse
	}
	return "pubsub." + universe + ":443"
}

// EffectiveUniverse returns the universe domain used for display.
func EffectiveUniverse(cfg config.Config) string {
	if cfg.EmulatorHost != "" {
		return ""
	}
	if cfg.UniverseDomain == "" {
		return defaultUniverse
	}
	return cfg.UniverseDomain
}

// AuthMode describes which credentials the process will use.
func AuthMode(cfg config.Config) string {
	switch {
	case cfg.EmulatorHost != "":
		return "emulator"
	case cfg.CredentialsFile != "":
		return "credentials-file"
	default:
		return "adc"
	}
}

// ClientOptions converts a plan into Google API client options.
func ClientOptions(cfg config.Config) ([]option.ClientOption, error) {
	plan, err := BuildPlan(cfg)
	if err != nil {
		return nil, err
	}
	if plan.Emulator {
		return []option.ClientOption{
			option.WithEndpoint(plan.Endpoint),
			option.WithoutAuthentication(),
			option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
			option.WithTelemetryDisabled(),
			option.WithUserAgent("pubsub-printer/" + version.Version),
		}, nil
	}

	opts := []option.ClientOption{
		option.WithUserAgent("pubsub-printer/" + version.Version),
	}
	if plan.UniverseDomain != "" {
		opts = append(opts, option.WithUniverseDomain(plan.UniverseDomain))
	}
	if plan.Endpoint != "" {
		opts = append(opts, option.WithEndpoint(plan.Endpoint))
	}
	if plan.CredentialsFile != "" {
		opts = append(opts, option.WithCredentialsFile(plan.CredentialsFile))
	}
	if plan.QuotaProject != "" {
		opts = append(opts, option.WithQuotaProject(plan.QuotaProject))
	}
	return opts, nil
}

// UniverseHint wraps a client constructor error that is caused by a universe mismatch.
func UniverseHint(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("create pubsub client: %w (set universe_domain or GOOGLE_CLOUD_UNIVERSE_DOMAIN, for example s3nsapis.fr, so it matches the credentials)", err)
}

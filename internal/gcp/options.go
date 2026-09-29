// Package gcp builds Google Cloud client options for a standard universe
// (googleapis.com) or a partner universe such as s3nsapis.fr.
package gcp

import (
	"fmt"

	"github.com/ybelleguic/pubsub-printer/internal/config"
	"github.com/ybelleguic/pubsub-printer/internal/version"
	"google.golang.org/api/option"
)

const defaultUniverse = "googleapis.com"

// Plan is the resolved dial plan. It is separate from the opaque client
// options so tests can check universe and endpoint selection directly.
type Plan struct {
	UniverseDomain  string
	Endpoint        string
	CredentialsFile string
}

// BuildPlan decides how the Pub/Sub client authenticates and which API host it uses.
//
// On GKE Workload Identity and Cloud Run, leave credentials empty. The client
// libraries then use Application Default Credentials and the metadata server.
// For a non-default universe, UniverseDomain must match the credentials
// (GOOGLE_CLOUD_UNIVERSE_DOMAIN, for example s3nsapis.fr).
func BuildPlan(cfg config.Config) Plan {
	return Plan{
		UniverseDomain:  cfg.UniverseDomain,
		Endpoint:        ResolveEndpoint(cfg.UniverseDomain),
		CredentialsFile: cfg.CredentialsFile,
	}
}

// ResolveEndpoint returns the Pub/Sub gRPC target.
// A non-default universe uses pubsub.<universe>:443, which is how s3nsapis.fr
// exposes pubsub.googleapis.com. The default universe keeps an empty endpoint
// so the client library can apply its own endpoint template.
func ResolveEndpoint(universe string) string {
	if universe == "" || universe == defaultUniverse {
		return ""
	}
	return "pubsub." + universe + ":443"
}

// EffectiveEndpoint is the host shown in logs and message metadata.
func EffectiveEndpoint(cfg config.Config) string {
	if endpoint := ResolveEndpoint(cfg.UniverseDomain); endpoint != "" {
		return endpoint
	}
	universe := cfg.UniverseDomain
	if universe == "" {
		universe = defaultUniverse
	}
	return "pubsub." + universe + ":443"
}

// EffectiveUniverse returns the universe domain used for display.
func EffectiveUniverse(cfg config.Config) string {
	if cfg.UniverseDomain == "" {
		return defaultUniverse
	}
	return cfg.UniverseDomain
}

// AuthMode describes which credentials the process will use.
func AuthMode(cfg config.Config) string {
	if cfg.CredentialsFile != "" {
		return "credentials-file"
	}
	return "adc"
}

// ClientOptions converts a plan into Google API client options.
func ClientOptions(cfg config.Config) []option.ClientOption {
	plan := BuildPlan(cfg)
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
	return opts
}

// UniverseHint wraps a client constructor error that is caused by a universe mismatch.
func UniverseHint(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("create pubsub client: %w (set universe_domain or GOOGLE_CLOUD_UNIVERSE_DOMAIN, for example s3nsapis.fr, so it matches the credentials)", err)
}

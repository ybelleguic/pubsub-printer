package gcp

import (
	"testing"

	"github.com/ybelleguic/pubsub-printer/internal/config"
)

func TestBuildPlanS3NS(t *testing.T) {
	plan := BuildPlan(config.Config{
		UniverseDomain: "s3nsapis.fr",
		ProjectID:      "s3ns:demo",
	})
	if plan.UniverseDomain != "s3nsapis.fr" {
		t.Fatalf("universe = %q", plan.UniverseDomain)
	}
	if plan.Endpoint != "pubsub.s3nsapis.fr:443" {
		t.Fatalf("endpoint = %q", plan.Endpoint)
	}
	if AuthMode(config.Config{UniverseDomain: "s3nsapis.fr"}) != "adc" {
		t.Fatal("expected adc for workload identity")
	}
	opts := ClientOptions(config.Config{UniverseDomain: "s3nsapis.fr"})
	if len(opts) != 3 {
		t.Fatalf("options = %d", len(opts))
	}
}

func TestBuildPlanCredentialsFile(t *testing.T) {
	cfg := config.Config{
		UniverseDomain:  "s3nsapis.fr",
		CredentialsFile: "/var/secrets/adc.json",
	}
	plan := BuildPlan(cfg)
	if plan.Endpoint != "pubsub.s3nsapis.fr:443" || plan.CredentialsFile != "/var/secrets/adc.json" {
		t.Fatalf("plan = %+v", plan)
	}
	if AuthMode(cfg) != "credentials-file" {
		t.Fatal(AuthMode(cfg))
	}
	opts := ClientOptions(cfg)
	if len(opts) != 4 {
		t.Fatalf("options = %d", len(opts))
	}
}

func TestBuildPlanDefaultUniverseLeavesEndpointEmpty(t *testing.T) {
	plan := BuildPlan(config.Config{})
	if plan.Endpoint != "" {
		t.Fatalf("endpoint = %q", plan.Endpoint)
	}
	if EffectiveEndpoint(config.Config{}) != "pubsub.googleapis.com:443" {
		t.Fatal(EffectiveEndpoint(config.Config{}))
	}
	if EffectiveUniverse(config.Config{}) != "googleapis.com" {
		t.Fatal(EffectiveUniverse(config.Config{}))
	}
}

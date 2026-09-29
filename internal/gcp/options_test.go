package gcp

import (
	"testing"

	"github.com/ybelleguic/pubsub-printer/internal/config"
)

func TestBuildPlanS3NS(t *testing.T) {
	plan, err := BuildPlan(config.Config{
		UniverseDomain: "s3nsapis.fr",
		ProjectID:      "s3ns:demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Emulator {
		t.Fatal("emulator")
	}
	if plan.UniverseDomain != "s3nsapis.fr" {
		t.Fatalf("universe = %q", plan.UniverseDomain)
	}
	if plan.Endpoint != "pubsub.s3nsapis.fr:443" {
		t.Fatalf("endpoint = %q", plan.Endpoint)
	}
	if AuthMode(config.Config{UniverseDomain: "s3nsapis.fr"}) != "adc" {
		t.Fatal("expected adc for workload identity")
	}
}

func TestBuildPlanExplicitEndpointAndCredentials(t *testing.T) {
	cfg := config.Config{
		UniverseDomain:  "s3nsapis.fr",
		Endpoint:        "pubsub.s3nsapis.fr:443",
		CredentialsFile: "/var/secrets/adc.json",
		QuotaProject:    "s3ns:demo",
	}
	plan, err := BuildPlan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Endpoint != "pubsub.s3nsapis.fr:443" || plan.CredentialsFile == "" || plan.QuotaProject == "" {
		t.Fatalf("plan = %+v", plan)
	}
	if AuthMode(cfg) != "credentials-file" {
		t.Fatal(AuthMode(cfg))
	}
	opts, err := ClientOptions(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) < 4 {
		t.Fatalf("options = %d", len(opts))
	}
}

func TestBuildPlanDefaultUniverseLeavesEndpointEmpty(t *testing.T) {
	plan, err := BuildPlan(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
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

func TestBuildPlanEmulatorSkipsUniverse(t *testing.T) {
	cfg := config.Config{EmulatorHost: "127.0.0.1:8085", UniverseDomain: "s3nsapis.fr"}
	plan, err := BuildPlan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Emulator || plan.Endpoint != "127.0.0.1:8085" || plan.UniverseDomain != "" {
		t.Fatalf("plan = %+v", plan)
	}
	if AuthMode(cfg) != "emulator" {
		t.Fatal(AuthMode(cfg))
	}
}

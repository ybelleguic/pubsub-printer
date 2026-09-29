package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{
		"project_id": "from-file",
		"subscription": "events",
		"universe_domain": "googleapis.com",
		"ack": true,
		"format": "pretty"
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(nil, []string{
		"PUBSUB_CONFIG=" + path,
		"PUBSUB_PROJECT_ID=s3ns:from-env",
		"GOOGLE_CLOUD_UNIVERSE_DOMAIN=s3nsapis.fr",
		"PUBSUB_ACK=false",
		"PUBSUB_FORMAT=json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProjectID != "s3ns:from-env" {
		t.Fatalf("project = %q", cfg.ProjectID)
	}
	if cfg.UniverseDomain != "s3nsapis.fr" {
		t.Fatalf("universe = %q", cfg.UniverseDomain)
	}
	if cfg.Ack {
		t.Fatal("expected ack to be disabled by the environment")
	}
	if cfg.Format != "json" {
		t.Fatalf("format = %q", cfg.Format)
	}
	if cfg.Subscription != "events" {
		t.Fatalf("subscription = %q", cfg.Subscription)
	}
	got, err := SubscriptionResource(cfg.ProjectID, cfg.Subscription)
	if err != nil {
		t.Fatal(err)
	}
	if got != "projects/s3ns:from-env/subscriptions/events" {
		t.Fatalf("resource = %q", got)
	}
}

func TestLoadFlagOverridesEnv(t *testing.T) {
	cfg, err := Load([]string{
		"--project=s3ns:from-flag",
		"--subscription=projects/s3ns:from-flag/subscriptions/events",
		"--universe-domain=s3nsapis.fr",
		"--endpoint=https://pubsub.s3nsapis.fr",
		"--ack=true",
		"--health-addr=-",
	}, []string{
		"PUBSUB_PROJECT_ID=from-env",
		"PUBSUB_SUBSCRIPTION=other",
		"PORT=8080",
		"GOOGLE_CLOUD_UNIVERSE_DOMAIN=googleapis.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProjectID != "s3ns:from-flag" {
		t.Fatalf("project = %q", cfg.ProjectID)
	}
	if cfg.UniverseDomain != "s3nsapis.fr" {
		t.Fatalf("universe = %q", cfg.UniverseDomain)
	}
	if cfg.Endpoint != "pubsub.s3nsapis.fr:443" {
		t.Fatalf("endpoint = %q", cfg.Endpoint)
	}
	if cfg.HealthAddr != "" {
		t.Fatalf("health addr = %q, want disabled despite PORT", cfg.HealthAddr)
	}
}

func TestLoadPortEnablesHealth(t *testing.T) {
	cfg, err := Load([]string{"--project=demo-project", "--subscription=events"}, []string{"PORT=8080"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HealthAddr != ":8080" {
		t.Fatalf("health = %q", cfg.HealthAddr)
	}
	if !cfg.Ack {
		t.Fatal("ack should default to true")
	}
	if cfg.Format != "pretty" {
		t.Fatalf("format = %q", cfg.Format)
	}
	if cfg.MaxOutstandingMessages != DefaultMaxOutstandingMessages {
		t.Fatalf("messages = %d", cfg.MaxOutstandingMessages)
	}
}

func TestLoadInfersProjectFromSubscription(t *testing.T) {
	cfg, err := Load([]string{
		"--subscription=projects/s3ns:inferred/subscriptions/events",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProjectID != "s3ns:inferred" {
		t.Fatalf("project = %q", cfg.ProjectID)
	}
}

func TestLoadRejectsUnknownJSONField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"projectId":"demo-project","subscription":"events"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load([]string{"--config=" + path}, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadExampleConfig(t *testing.T) {
	cfg, err := Load([]string{"--config=../../config.example.json"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UniverseDomain != "s3nsapis.fr" || cfg.ProjectID != "s3ns:my-project" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadInvalidValues(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  []string
		want string
	}{
		{name: "missing subscription", args: []string{"--project=demo-project"}, want: "subscription is required"},
		{name: "bad universe", args: []string{"--project=demo-project", "--subscription=events", "--universe-domain=https://s3nsapis.fr"}, want: "bare host"},
		{name: "bad format", args: []string{"--project=demo-project", "--subscription=events", "--format=yaml"}, want: "format"},
		{name: "bad ack", env: []string{"PUBSUB_PROJECT_ID=demo-project", "PUBSUB_SUBSCRIPTION=events", "PUBSUB_ACK=maybe"}, want: "PUBSUB_ACK"},
		{name: "endpoint path", args: []string{"--project=demo-project", "--subscription=events", "--endpoint=https://pubsub.s3nsapis.fr/v1"}, want: "path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(tc.args, tc.env)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestHelpAndVersion(t *testing.T) {
	_, err := Load([]string{"--help"}, nil)
	var help *HelpError
	if err == nil || !errorsAs(err, &help) || !strings.Contains(help.Usage, "pubsub-printer") {
		t.Fatalf("help = %v", err)
	}
	_, err = Load([]string{"--version"}, nil)
	if err != ErrVersion {
		t.Fatalf("version err = %v", err)
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	cases := map[string]string{
		"":                                "",
		"pubsub.s3nsapis.fr":              "pubsub.s3nsapis.fr:443",
		"pubsub.s3nsapis.fr:443":          "pubsub.s3nsapis.fr:443",
		"https://pubsub.s3nsapis.fr":      "pubsub.s3nsapis.fr:443",
		"https://pubsub.s3nsapis.fr:8443": "pubsub.s3nsapis.fr:8443",
	}
	for in, want := range cases {
		got, err := NormalizeEndpoint(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got != want {
			t.Fatalf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestNormalizeUniverse(t *testing.T) {
	got, err := NormalizeUniverse("S3NSAPIS.FR.")
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3nsapis.fr" {
		t.Fatalf("got %q", got)
	}
	if _, err := NormalizeUniverse("not a domain"); err == nil {
		t.Fatal("expected error")
	}
}

func errorsAs(err error, target **HelpError) bool {
	if err == nil {
		return false
	}
	h, ok := err.(*HelpError)
	if !ok {
		return false
	}
	*target = h
	return true
}

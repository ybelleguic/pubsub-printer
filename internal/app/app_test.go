package app

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/pubsub/pstest"
	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/ybelleguic/pubsub-printer/internal/config"
	"github.com/ybelleguic/pubsub-printer/internal/gcp"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestRunPrintsAndAcks(t *testing.T) {
	cfg, srv, client := newEmulator(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	publisher := client.Publisher("events")
	t.Cleanup(publisher.Stop)
	publishedID, err := publisher.Publish(ctx, &pubsub.Message{
		Data:       []byte(`{"hello":"s3ns"}`),
		Attributes: map[string]string{"event": "demo"},
	}).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var out lockedBuffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, cfg, &out, nil)
	}()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(out.String(), publishedID) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return after cancel")
	}

	raw := strings.TrimSpace(out.String())
	if raw == "" {
		t.Fatal("no output")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.Split(raw, "\n")[0]), &decoded); err != nil {
		t.Fatalf("output %q: %v", raw, err)
	}
	if decoded["id"] != publishedID {
		t.Fatalf("id = %#v", decoded["id"])
	}
	if decoded["project"] != "demo-project" || decoded["subscription"] != "projects/demo-project/subscriptions/events" {
		t.Fatalf("decoded = %#v", decoded)
	}
	if decoded["endpoint"] != gcp.EffectiveEndpoint(cfg) {
		t.Fatalf("endpoint = %#v", decoded["endpoint"])
	}
	data, ok := decoded["data"].(map[string]any)
	if !ok || data["hello"] != "s3ns" {
		t.Fatalf("data = %#v", decoded["data"])
	}
	attrs := decoded["attributes"].(map[string]any)
	if attrs["event"] != "demo" {
		t.Fatalf("attributes = %#v", attrs)
	}

	acked := false
	for _, msg := range srv.Messages() {
		if msg.ID == publishedID && msg.Acks > 0 {
			acked = true
		}
	}
	if !acked {
		t.Fatal("message was not acked")
	}
}

func newEmulator(t *testing.T) (config.Config, *pstest.Server, *pubsub.Client) {
	t.Helper()
	srv := pstest.NewServer()
	t.Cleanup(func() { _ = srv.Close() })

	cfg := config.Config{
		ProjectID:              "demo-project",
		Subscription:           "events",
		Format:                 "json",
		Ack:                    true,
		MaxOutstandingMessages: 10,
		MaxOutstandingBytes:    1 << 20,
	}
	// The Pub/Sub client library dials this host itself. It is not an
	// application setting.
	t.Setenv("PUBSUB_EMULATOR_HOST", srv.Addr)

	opts := []option.ClientOption{
		option.WithEndpoint(srv.Addr),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
		option.WithTelemetryDisabled(),
	}
	client, err := pubsub.NewClient(context.Background(), cfg.ProjectID, opts...)
	if err != nil {
		t.Fatalf("setup client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	topic := "projects/demo-project/topics/events"
	sub := "projects/demo-project/subscriptions/events"
	if _, err := client.TopicAdminClient.CreateTopic(context.Background(), &pubsubpb.Topic{Name: topic}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SubscriptionAdminClient.CreateSubscription(context.Background(), &pubsubpb.Subscription{
		Name:  sub,
		Topic: topic,
	}); err != nil {
		t.Fatal(err)
	}
	return cfg, srv, client
}

type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

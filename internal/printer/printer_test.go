package printer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPrettyJSONPayloadAndMetadata(t *testing.T) {
	attempt := 2
	var buf bytes.Buffer
	p := New(&buf, "pretty", 0)
	published := time.Date(2026, 9, 29, 11, 49, 58, 0, time.UTC)
	received := published.Add(2 * time.Second)
	err := p.Print(Record{
		Project:      "s3ns:demo",
		Subscription: "projects/s3ns:demo/subscriptions/events",
		Universe:     "s3nsapis.fr",
		Endpoint:     "pubsub.s3nsapis.fr:443",
		ID:           "m-1",
		Data:         []byte("{\n\"hello\": \"s3ns\"}"),
		Attributes:   map[string]string{"source": "api", "event": "user.created"},
		PublishTime:  published,
		ReceivedAt:   received,
		OrderingKey:  "user-42",
		Attempt:      &attempt,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{
		"┌─ m-1",
		"project        s3ns:demo",
		"subscription   projects/s3ns:demo/subscriptions/events",
		"universe       s3nsapis.fr",
		"endpoint       pubsub.s3nsapis.fr:443",
		"published      2026-09-29T11:49:58.000Z",
		"received       2026-09-29T11:50:00.000Z",
		"ordering_key   user-42",
		"attempt        2",
		"event          user.created",
		"source         api",
		"payload        ",
		"json",
		`"hello": "s3ns"`,
		"└─",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	event := strings.Index(got, "event")
	source := strings.Index(got, "source")
	if event < 0 || source < event {
		t.Fatalf("attributes are not sorted:\n%s", got)
	}
}

func TestPrettyBinaryTruncation(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf, "pretty", 4)
	if err := p.Print(Record{ID: "bin", Data: []byte{0xff, 0xfe, 0xfd, 0xfc, 0xfb}}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "binary") || !strings.Contains(got, "truncated to 4 B") {
		t.Fatalf("output:\n%s", got)
	}
	if !strings.Contains(got, "attributes     -") {
		t.Fatalf("missing empty attributes:\n%s", got)
	}
}

func TestJSONLine(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf, "json", 0)
	attempt := 1
	err := p.Print(Record{
		Project:      "s3ns:demo",
		Subscription: "projects/s3ns:demo/subscriptions/events",
		Universe:     "s3nsapis.fr",
		Endpoint:     "pubsub.s3nsapis.fr:443",
		ID:           "m-2",
		Data:         []byte(`{"n":1}`),
		Attributes:   map[string]string{"event": "ping"},
		PublishTime:  time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC),
		ReceivedAt:   time.Date(2026, 9, 29, 12, 0, 1, 0, time.UTC),
		Attempt:      &attempt,
	})
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(buf.String())
	if strings.Contains(line, "\n") {
		t.Fatalf("json output is not a single line:\n%s", buf.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(line), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["id"] != "m-2" || decoded["universe"] != "s3nsapis.fr" {
		t.Fatalf("decoded = %#v", decoded)
	}
	data, ok := decoded["data"].(map[string]any)
	if !ok || data["n"] != float64(1) {
		t.Fatalf("data = %#v", decoded["data"])
	}
	if decoded["delivery_attempt"] != float64(1) {
		t.Fatalf("attempt = %#v", decoded["delivery_attempt"])
	}
	attrs := decoded["attributes"].(map[string]any)
	if attrs["event"] != "ping" {
		t.Fatalf("attrs = %#v", attrs)
	}
}

func TestConcurrentPrintsDoNotInterleave(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf, "json", 0)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := p.Print(Record{ID: fmt.Sprintf("id-%02d", i), Data: []byte("x")}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 20 {
		t.Fatalf("lines = %d\n%s", len(lines), buf.String())
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("broken line %s", line)
		}
	}
}

func TestJSONTextAndEmpty(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf, "json", 0)
	if err := p.Print(Record{ID: "t", Data: []byte("hello")}); err != nil {
		t.Fatal(err)
	}
	if err := p.Print(Record{ID: "e"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d\n%s", len(lines), buf.String())
	}
	var text map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &text); err != nil {
		t.Fatal(err)
	}
	if text["data_text"] != "hello" || text["payload_kind"] != "text" {
		t.Fatalf("text = %#v", text)
	}
	var empty map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &empty); err != nil {
		t.Fatal(err)
	}
	if empty["payload_kind"] != "empty" || empty["data_bytes"] != float64(0) {
		t.Fatalf("empty = %#v", empty)
	}
}

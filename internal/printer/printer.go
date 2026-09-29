// Package printer renders Pub/Sub messages to stdout.
package printer

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Record is one received Pub/Sub message plus the connection metadata that
// explains where it came from.
type Record struct {
	Project      string
	Subscription string
	Universe     string
	Endpoint     string
	ID           string
	Data         []byte
	Attributes   map[string]string
	PublishTime  time.Time
	ReceivedAt   time.Time
	OrderingKey  string
	Attempt      *int
}

// Printer writes records without interleaving concurrent messages.
type Printer struct {
	out     io.Writer
	format  string
	maxBody int
	mu      sync.Mutex
}

// New builds a printer. format is "pretty" or "json".
// maxBody is the maximum payload bytes rendered; 0 means no truncation.
func New(out io.Writer, format string, maxBody int) *Printer {
	if out == nil {
		out = os.Stdout
	}
	return &Printer{
		out:     out,
		format:  format,
		maxBody: maxBody,
	}
}

// Print writes one record followed by a newline.
func (p *Printer) Print(rec Record) error {
	var buf bytes.Buffer
	var err error
	if p.format == "json" {
		err = writeJSON(&buf, rec, p.maxBody)
	} else {
		err = p.writePretty(&buf, rec)
	}
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err = p.out.Write(buf.Bytes())
	return err
}

func (p *Printer) writePretty(buf *bytes.Buffer, rec Record) error {
	id := rec.ID
	if id == "" {
		id = "-"
	}
	fmt.Fprintf(buf, "┌─ %s\n", id)
	p.field(buf, "project", dash(rec.Project))
	p.field(buf, "subscription", dash(rec.Subscription))
	p.field(buf, "universe", dash(rec.Universe))
	p.field(buf, "endpoint", dash(rec.Endpoint))
	p.field(buf, "published", formatTime(rec.PublishTime))
	p.field(buf, "received", formatTime(rec.ReceivedAt))
	p.field(buf, "ordering_key", dash(rec.OrderingKey))
	p.field(buf, "attempt", formatAttempt(rec.Attempt))
	p.writeAttributes(buf, rec.Attributes)

	body, kind, truncated := renderBody(rec.Data, p.maxBody)
	label := fmt.Sprintf("%d B · %s", len(rec.Data), kind)
	if truncated {
		label = fmt.Sprintf("%d B · %s · truncated to %d B", len(rec.Data), kind, bodyLimit(rec.Data, p.maxBody))
	}
	p.field(buf, "payload", label)
	fmt.Fprintf(buf, "└─\n")
	if body != "" {
		buf.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			buf.WriteByte('\n')
		}
	}
	buf.WriteByte('\n')
	return nil
}

func (p *Printer) field(buf *bytes.Buffer, key, value string) {
	fmt.Fprintf(buf, "│ %-14s %s\n", key, value)
}

func (p *Printer) writeAttributes(buf *bytes.Buffer, attrs map[string]string) {
	if len(attrs) == 0 {
		p.field(buf, "attributes", "-")
		return
	}
	fmt.Fprintf(buf, "│ attributes\n")
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		value := strings.ReplaceAll(attrs[k], "\n", `\n`)
		fmt.Fprintf(buf, "│   %-14s %s\n", k, value)
	}
}

func writeJSON(buf *bytes.Buffer, rec Record, maxBody int) error {
	body, kind, truncated := renderBody(rec.Data, maxBody)
	attrs := rec.Attributes
	if attrs == nil {
		attrs = map[string]string{}
	}
	out := map[string]any{
		"id":            rec.ID,
		"project":       rec.Project,
		"subscription":  rec.Subscription,
		"universe":      rec.Universe,
		"endpoint":      rec.Endpoint,
		"publish_time":  formatTime(rec.PublishTime),
		"received_time": formatTime(rec.ReceivedAt),
		"ordering_key":  rec.OrderingKey,
		"attributes":    attrs,
		"data_bytes":    len(rec.Data),
		"payload_kind":  kind,
	}
	if rec.Attempt != nil {
		out["delivery_attempt"] = *rec.Attempt
	}
	if truncated {
		out["truncated"] = true
	}
	switch kind {
	case "json":
		raw := json.RawMessage(bytes.TrimSpace(limited(rec.Data, maxBody)))
		out["data"] = raw
	case "text", "empty":
		if kind == "text" {
			out["data_text"] = strings.TrimSuffix(body, "\n")
		}
	default:
		out["data_base64"] = base64.StdEncoding.EncodeToString(limited(rec.Data, maxBody))
	}

	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return err
	}
	return nil
}

func renderBody(data []byte, maxBody int) (body, kind string, truncated bool) {
	if len(data) == 0 {
		return "", "empty", false
	}
	shown := limited(data, maxBody)
	truncated = len(shown) < len(data)
	if !truncated && json.Valid(data) {
		var indented bytes.Buffer
		if err := json.Indent(&indented, data, "", "  "); err == nil {
			indented.WriteByte('\n')
			return indented.String(), "json", false
		}
	}
	if utf8.Valid(shown) {
		return string(shown), "text", truncated
	}
	encoded := base64.StdEncoding.EncodeToString(shown)
	var b strings.Builder
	for len(encoded) > 76 {
		b.WriteString(encoded[:76])
		b.WriteByte('\n')
		encoded = encoded[76:]
	}
	b.WriteString(encoded)
	b.WriteByte('\n')
	return b.String(), "binary", truncated
}

func limited(data []byte, maxBody int) []byte {
	if maxBody > 0 && len(data) > maxBody {
		return data[:maxBody]
	}
	return data
}

func bodyLimit(data []byte, maxBody int) int {
	return len(limited(data, maxBody))
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func formatAttempt(attempt *int) string {
	if attempt == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *attempt)
}

func dash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}

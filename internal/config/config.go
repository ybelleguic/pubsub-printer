// Package config loads pubsub-printer settings from a JSON file, the
// environment, and command-line flags.
//
// Precedence is flags, then environment variables, then the JSON file, then
// built-in defaults. A flag that was passed, or an environment variable that
// is present, replaces the lower-priority value.
package config

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	// DefaultMaxOutstandingMessages bounds how many messages are held in memory.
	DefaultMaxOutstandingMessages = 100
	// DefaultMaxOutstandingBytes bounds the payload bytes held in memory (64 MiB).
	DefaultMaxOutstandingBytes = 64 << 20
	// DefaultMaxBodyBytes truncates the bytes rendered for a single message.
	DefaultMaxBodyBytes = 256 << 10

	defaultFormat = "pretty"
)

// ErrVersion is returned when --version is set. The process should print the
// version and exit successfully.
var ErrVersion = errors.New("version")

// HelpError is returned when --help is set. Usage is the text to print.
type HelpError struct {
	Usage string
}

func (e *HelpError) Error() string { return e.Usage }

// Config is the resolved runtime configuration.
type Config struct {
	ProjectID              string
	Subscription           string
	UniverseDomain         string
	CredentialsFile        string
	Format                 string
	Ack                    bool
	MaxOutstandingMessages int
	MaxOutstandingBytes    int
	MaxBodyBytes           int
	ExactlyOnce            bool
}

type fileConfig struct {
	ProjectID              *string `json:"project_id"`
	Subscription           *string `json:"subscription"`
	UniverseDomain         *string `json:"universe_domain"`
	Format                 *string `json:"format"`
	Ack                    *bool   `json:"ack"`
	MaxOutstandingMessages *int    `json:"max_outstanding_messages"`
	MaxOutstandingBytes    *int    `json:"max_outstanding_bytes"`
	MaxBodyBytes           *int    `json:"max_body_bytes"`
	ExactlyOnce            *bool   `json:"exactly_once"`
}

// Load resolves configuration from args and environ. args is os.Args[1:].
// environ contains entries in KEY=VALUE form, as returned by os.Environ.
func Load(args []string, environ []string) (Config, error) {
	env := envMap(environ)

	fs := flag.NewFlagSet("pubsub-printer", flag.ContinueOnError)
	var usage strings.Builder
	fs.SetOutput(&usage)
	flags := bindFlags(fs)
	fs.Usage = func() {
		fmt.Fprintf(&usage, "Usage: pubsub-printer [flags]\n\n")
		fmt.Fprintf(&usage, "Consume one Pub/Sub subscription and write each message to stdout.\n")
		fmt.Fprintf(&usage, "Settings come from flags, then environment variables, then --config JSON.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Config{}, &HelpError{Usage: usage.String()}
		}
		return Config{}, err
	}
	if flags.version {
		return Config{}, ErrVersion
	}
	if extra := fs.Args(); len(extra) > 0 {
		return Config{}, fmt.Errorf("unexpected arguments: %s", strings.Join(extra, " "))
	}

	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })

	cfg := defaults()

	configPath := strings.TrimSpace(flags.config)
	if !visited["config"] {
		configPath = strings.TrimSpace(env["PUBSUB_CONFIG"])
	}
	if configPath != "" {
		fileCfg, err := loadFile(configPath)
		if err != nil {
			return Config{}, err
		}
		applyFile(&cfg, fileCfg)
	}

	if err := applyEnv(&cfg, env); err != nil {
		return Config{}, err
	}
	if err := applyFlags(&cfg, flags, visited); err != nil {
		return Config{}, err
	}
	cfg.inferProject()
	if err := cfg.normalize(); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func defaults() Config {
	return Config{
		Format:                 defaultFormat,
		Ack:                    true,
		MaxOutstandingMessages: DefaultMaxOutstandingMessages,
		MaxOutstandingBytes:    DefaultMaxOutstandingBytes,
		MaxBodyBytes:           DefaultMaxBodyBytes,
	}
}

type cliFlags struct {
	config       string
	project      string
	subscription string
	universe     string
	format       string
	ack          string
	maxMessages  int
	maxBytes     int
	maxBody      int
	exactlyOnce  string
	version      bool
}

func bindFlags(fs *flag.FlagSet) *cliFlags {
	f := &cliFlags{}
	fs.StringVar(&f.config, "config", "", "JSON config file. Also read from PUBSUB_CONFIG")
	fs.StringVar(&f.project, "project", "", "GCP project id. On S3NS this includes the universe prefix, for example s3ns:my-project. Also PROJECT_ID")
	fs.StringVar(&f.subscription, "subscription", "", "Subscription id or projects/{project}/subscriptions/{id}")
	fs.StringVar(&f.universe, "universe-domain", "", "Universe domain, for example s3nsapis.fr. Also GOOGLE_CLOUD_UNIVERSE_DOMAIN")
	fs.StringVar(&f.format, "format", "", "Output format: pretty or json")
	fs.StringVar(&f.ack, "ack", "", "Acknowledge messages after printing (true or false). false nacks them")
	fs.IntVar(&f.maxMessages, "max-outstanding-messages", 0, "Maximum messages buffered locally")
	fs.IntVar(&f.maxBytes, "max-outstanding-bytes", 0, "Maximum payload bytes buffered locally")
	fs.IntVar(&f.maxBody, "max-body-bytes", 0, "Maximum payload bytes written per message; 0 means no truncation")
	fs.StringVar(&f.exactlyOnce, "exactly-once", "", "Wait for exactly-once ack results (true or false)")
	fs.BoolVar(&f.version, "version", false, "Print the version and exit")
	return f
}

func loadFile(path string) (fileConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return fileConfig{}, fmt.Errorf("open config %s: %w", path, err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var cfg fileConfig
	if err := dec.Decode(&cfg); err != nil {
		return fileConfig{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fileConfig{}, fmt.Errorf("parse config %s: multiple JSON values", path)
		}
		return fileConfig{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}

func applyFile(cfg *Config, file fileConfig) {
	setString(&cfg.ProjectID, file.ProjectID)
	setString(&cfg.Subscription, file.Subscription)
	setString(&cfg.UniverseDomain, file.UniverseDomain)
	setString(&cfg.Format, file.Format)
	if file.Ack != nil {
		cfg.Ack = *file.Ack
	}
	if file.ExactlyOnce != nil {
		cfg.ExactlyOnce = *file.ExactlyOnce
	}
	if file.MaxOutstandingMessages != nil {
		cfg.MaxOutstandingMessages = *file.MaxOutstandingMessages
	}
	if file.MaxOutstandingBytes != nil {
		cfg.MaxOutstandingBytes = *file.MaxOutstandingBytes
	}
	if file.MaxBodyBytes != nil {
		cfg.MaxBodyBytes = *file.MaxBodyBytes
	}
}

func setString(dst *string, src *string) {
	if src != nil {
		*dst = *src
	}
}

func applyEnv(cfg *Config, env map[string]string) error {
	if v, ok := env["PROJECT_ID"]; ok {
		cfg.ProjectID = v
	}
	if v, ok := env["PUBSUB_SUBSCRIPTION"]; ok {
		cfg.Subscription = v
	}
	if v, ok := env["GOOGLE_CLOUD_UNIVERSE_DOMAIN"]; ok {
		cfg.UniverseDomain = v
	}
	if v, ok := env["GOOGLE_APPLICATION_CREDENTIALS"]; ok {
		cfg.CredentialsFile = v
	}
	if v, ok := env["PUBSUB_FORMAT"]; ok {
		cfg.Format = v
	}
	if v, ok := env["PUBSUB_ACK"]; ok {
		b, err := parseBool(v)
		if err != nil {
			return fmt.Errorf("PUBSUB_ACK: %w", err)
		}
		cfg.Ack = b
	}
	if v, ok := env["PUBSUB_MAX_OUTSTANDING_MESSAGES"]; ok {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("PUBSUB_MAX_OUTSTANDING_MESSAGES: %w", err)
		}
		cfg.MaxOutstandingMessages = n
	}
	if v, ok := env["PUBSUB_MAX_OUTSTANDING_BYTES"]; ok {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("PUBSUB_MAX_OUTSTANDING_BYTES: %w", err)
		}
		cfg.MaxOutstandingBytes = n
	}
	if v, ok := env["PUBSUB_MAX_BODY_BYTES"]; ok {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("PUBSUB_MAX_BODY_BYTES: %w", err)
		}
		cfg.MaxBodyBytes = n
	}
	if v, ok := env["PUBSUB_EXACTLY_ONCE"]; ok {
		b, err := parseBool(v)
		if err != nil {
			return fmt.Errorf("PUBSUB_EXACTLY_ONCE: %w", err)
		}
		cfg.ExactlyOnce = b
	}
	return nil
}

func applyFlags(cfg *Config, f *cliFlags, visited map[string]bool) error {
	if visited["project"] {
		cfg.ProjectID = f.project
	}
	if visited["subscription"] {
		cfg.Subscription = f.subscription
	}
	if visited["universe-domain"] {
		cfg.UniverseDomain = f.universe
	}
	if visited["format"] {
		cfg.Format = f.format
	}
	if visited["ack"] {
		b, err := parseBool(f.ack)
		if err != nil {
			return fmt.Errorf("--ack: %w", err)
		}
		cfg.Ack = b
	}
	if visited["max-outstanding-messages"] {
		cfg.MaxOutstandingMessages = f.maxMessages
	}
	if visited["max-outstanding-bytes"] {
		cfg.MaxOutstandingBytes = f.maxBytes
	}
	if visited["max-body-bytes"] {
		cfg.MaxBodyBytes = f.maxBody
	}
	if visited["exactly-once"] {
		b, err := parseBool(f.exactlyOnce)
		if err != nil {
			return fmt.Errorf("--exactly-once: %w", err)
		}
		cfg.ExactlyOnce = b
	}
	return nil
}

func (c *Config) inferProject() {
	if strings.TrimSpace(c.ProjectID) != "" {
		return
	}
	project, _, ok := splitSubscription(c.Subscription)
	if ok {
		c.ProjectID = project
	}
}

func (c *Config) normalize() error {
	var err error
	c.ProjectID = strings.TrimSpace(c.ProjectID)
	c.Subscription = strings.TrimSpace(c.Subscription)
	c.CredentialsFile = strings.TrimSpace(c.CredentialsFile)
	c.Format = strings.ToLower(strings.TrimSpace(c.Format))
	if c.Format == "" {
		c.Format = defaultFormat
	}
	c.UniverseDomain, err = NormalizeUniverse(c.UniverseDomain)
	if err != nil {
		return err
	}
	if c.MaxOutstandingMessages == 0 {
		c.MaxOutstandingMessages = DefaultMaxOutstandingMessages
	}
	if c.MaxOutstandingBytes == 0 {
		c.MaxOutstandingBytes = DefaultMaxOutstandingBytes
	}
	return nil
}

// Validate checks the resolved configuration.
func (c Config) Validate() error {
	var errs []error
	if c.ProjectID == "" {
		errs = append(errs, errors.New("project_id is required (flag --project, PROJECT_ID, or config file)"))
	} else if !projectRE.MatchString(c.ProjectID) {
		errs = append(errs, fmt.Errorf("project_id %q is invalid", c.ProjectID))
	}
	if c.Subscription == "" {
		errs = append(errs, errors.New("subscription is required (flag --subscription, PUBSUB_SUBSCRIPTION, or config file)"))
	} else if _, err := SubscriptionResource(c.ProjectID, c.Subscription); err != nil {
		errs = append(errs, err)
	}
	switch c.Format {
	case "pretty", "json":
	default:
		errs = append(errs, fmt.Errorf("format must be pretty or json, got %q", c.Format))
	}
	if c.MaxOutstandingMessages < 1 {
		errs = append(errs, fmt.Errorf("max_outstanding_messages must be >= 1, got %d", c.MaxOutstandingMessages))
	}
	if c.MaxOutstandingBytes < 1 {
		errs = append(errs, fmt.Errorf("max_outstanding_bytes must be >= 1, got %d", c.MaxOutstandingBytes))
	}
	if c.MaxBodyBytes < 0 {
		errs = append(errs, fmt.Errorf("max_body_bytes must be >= 0, got %d", c.MaxBodyBytes))
	}
	return errors.Join(errs...)
}

// SubscriptionResource returns the fully qualified subscription name.
func SubscriptionResource(projectID, subscription string) (string, error) {
	subscription = strings.TrimSpace(subscription)
	if subscription == "" {
		return "", errors.New("subscription is empty")
	}
	if project, sub, ok := splitSubscription(subscription); ok {
		if !projectRE.MatchString(project) {
			return "", fmt.Errorf("subscription project %q is invalid", project)
		}
		if !subscriptionRE.MatchString(sub) {
			return "", fmt.Errorf("subscription id %q is invalid", sub)
		}
		return "projects/" + project + "/subscriptions/" + sub, nil
	}
	if strings.Contains(subscription, "/") {
		return "", fmt.Errorf("subscription %q must be an id or projects/{project}/subscriptions/{id}", subscription)
	}
	if !subscriptionRE.MatchString(subscription) {
		return "", fmt.Errorf("subscription id %q is invalid", subscription)
	}
	if projectID == "" {
		return "", errors.New("project_id is required to expand a short subscription id")
	}
	return "projects/" + projectID + "/subscriptions/" + subscription, nil
}

func splitSubscription(name string) (project, subscription string, ok bool) {
	parts := strings.Split(strings.TrimSpace(name), "/")
	if len(parts) != 4 || parts[0] != "projects" || parts[2] != "subscriptions" || parts[1] == "" || parts[3] == "" {
		return "", "", false
	}
	return parts[1], parts[3], true
}

var (
	projectRE      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,200}$`)
	subscriptionRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.~+%_-]{2,254}$`)
	universeRE     = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)
)

// NormalizeUniverse returns a bare universe domain such as s3nsapis.fr.
func NormalizeUniverse(raw string) (string, error) {
	u := strings.ToLower(strings.TrimSpace(raw))
	u = strings.TrimSuffix(u, ".")
	if u == "" {
		return "", nil
	}
	if strings.Contains(u, "://") || strings.Contains(u, "/") || strings.Contains(u, ":") {
		return "", fmt.Errorf("universe_domain must be a bare host such as s3nsapis.fr, got %q", raw)
	}
	if !universeRE.MatchString(u) {
		return "", fmt.Errorf("universe_domain %q is not a valid host", raw)
	}
	return u, nil
}

func envMap(environ []string) map[string]string {
	env := make(map[string]string, len(environ))
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		env[key] = value
	}
	return env
}

func parseBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "t", "true", "y", "yes", "on":
		return true, nil
	case "0", "f", "false", "n", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean %q", raw)
	}
}

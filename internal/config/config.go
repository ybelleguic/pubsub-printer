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
	"net"
	"net/url"
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
	// DefaultNumGoroutines is the number of StreamingPull callback goroutines.
	DefaultNumGoroutines = 4
	// DefaultMaxBodyBytes truncates the bytes rendered for a single message.
	DefaultMaxBodyBytes = 256 << 10

	defaultFormat = "pretty"
	defaultColor  = "auto"
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
	Endpoint               string
	CredentialsFile        string
	QuotaProject           string
	Format                 string
	Ack                    bool
	Color                  string
	MaxOutstandingMessages int
	MaxOutstandingBytes    int
	NumGoroutines          int
	MaxBodyBytes           int
	ExactlyOnce            bool
	HealthAddr             string
	EmulatorHost           string
}

type fileConfig struct {
	ProjectID              *string `json:"project_id"`
	Subscription           *string `json:"subscription"`
	UniverseDomain         *string `json:"universe_domain"`
	Endpoint               *string `json:"endpoint"`
	CredentialsFile        *string `json:"credentials_file"`
	QuotaProject           *string `json:"quota_project"`
	Format                 *string `json:"format"`
	Ack                    *bool   `json:"ack"`
	Color                  *string `json:"color"`
	MaxOutstandingMessages *int    `json:"max_outstanding_messages"`
	MaxOutstandingBytes    *int    `json:"max_outstanding_bytes"`
	NumGoroutines          *int    `json:"num_goroutines"`
	MaxBodyBytes           *int    `json:"max_body_bytes"`
	ExactlyOnce            *bool   `json:"exactly_once"`
	HealthAddr             *string `json:"health_addr"`
	EmulatorHost           *string `json:"emulator_host"`
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
	cfg.applyHealthDefault(env["PORT"], isHealthOff(cfg.HealthAddr))
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
		Color:                  defaultColor,
		MaxOutstandingMessages: DefaultMaxOutstandingMessages,
		MaxOutstandingBytes:    DefaultMaxOutstandingBytes,
		NumGoroutines:          DefaultNumGoroutines,
		MaxBodyBytes:           DefaultMaxBodyBytes,
	}
}

type cliFlags struct {
	config       string
	project      string
	subscription string
	universe     string
	endpoint     string
	credentials  string
	quota        string
	format       string
	ack          string
	color        string
	maxMessages  int
	maxBytes     int
	goroutines   int
	maxBody      int
	exactlyOnce  string
	health       string
	emulator     string
	version      bool
}

func bindFlags(fs *flag.FlagSet) *cliFlags {
	f := &cliFlags{}
	fs.StringVar(&f.config, "config", "", "JSON config file. Also read from PUBSUB_CONFIG")
	fs.StringVar(&f.project, "project", "", "GCP project id. On S3NS this includes the universe prefix, for example s3ns:my-project")
	fs.StringVar(&f.subscription, "subscription", "", "Subscription id or projects/{project}/subscriptions/{id}")
	fs.StringVar(&f.universe, "universe-domain", "", "Universe domain, for example s3nsapis.fr. Also GOOGLE_CLOUD_UNIVERSE_DOMAIN")
	fs.StringVar(&f.endpoint, "endpoint", "", "Optional Pub/Sub API host:port. Derived from the universe when omitted")
	fs.StringVar(&f.credentials, "credentials", "", "Optional credentials JSON file. Leave empty on GKE and Cloud Run to use Workload Identity")
	fs.StringVar(&f.quota, "quota-project", "", "Quota project passed to the client libraries")
	fs.StringVar(&f.format, "format", "", "Output format: pretty or json")
	fs.StringVar(&f.ack, "ack", "", "Acknowledge messages after printing (true or false). false nacks them")
	fs.StringVar(&f.color, "color", "", "Color mode: auto, always, or never")
	fs.IntVar(&f.maxMessages, "max-outstanding-messages", 0, "Maximum messages buffered locally")
	fs.IntVar(&f.maxBytes, "max-outstanding-bytes", 0, "Maximum payload bytes buffered locally")
	fs.IntVar(&f.goroutines, "num-goroutines", 0, "StreamingPull callback goroutines")
	fs.IntVar(&f.maxBody, "max-body-bytes", 0, "Maximum payload bytes written per message; 0 means no truncation")
	fs.StringVar(&f.exactlyOnce, "exactly-once", "", "Wait for exactly-once ack results (true or false)")
	fs.StringVar(&f.health, "health-addr", "", "Health listen address. \"-\" disables it. Defaults to :$PORT when PORT is set")
	fs.StringVar(&f.emulator, "emulator-host", "", "Pub/Sub emulator host:port. Also PUBSUB_EMULATOR_HOST")
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
	setString(&cfg.Endpoint, file.Endpoint)
	setString(&cfg.CredentialsFile, file.CredentialsFile)
	setString(&cfg.QuotaProject, file.QuotaProject)
	setString(&cfg.Format, file.Format)
	setString(&cfg.Color, file.Color)
	setString(&cfg.HealthAddr, file.HealthAddr)
	setString(&cfg.EmulatorHost, file.EmulatorHost)
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
	if file.NumGoroutines != nil {
		cfg.NumGoroutines = *file.NumGoroutines
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
	if v, ok := firstEnv(env, "PUBSUB_PROJECT_ID", "GOOGLE_CLOUD_PROJECT"); ok {
		cfg.ProjectID = v
	}
	if v, ok := env["PUBSUB_SUBSCRIPTION"]; ok {
		cfg.Subscription = v
	}
	if v, ok := firstEnv(env, "PUBSUB_UNIVERSE_DOMAIN", "GOOGLE_CLOUD_UNIVERSE_DOMAIN"); ok {
		cfg.UniverseDomain = v
	}
	if v, ok := env["PUBSUB_ENDPOINT"]; ok {
		cfg.Endpoint = v
	}
	if v, ok := firstEnv(env, "PUBSUB_CREDENTIALS_FILE", "GOOGLE_APPLICATION_CREDENTIALS"); ok {
		cfg.CredentialsFile = v
	}
	if v, ok := firstEnv(env, "PUBSUB_QUOTA_PROJECT", "GOOGLE_CLOUD_QUOTA_PROJECT"); ok {
		cfg.QuotaProject = v
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
	if v, ok := env["PUBSUB_COLOR"]; ok {
		cfg.Color = v
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
	if v, ok := env["PUBSUB_NUM_GOROUTINES"]; ok {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("PUBSUB_NUM_GOROUTINES: %w", err)
		}
		cfg.NumGoroutines = n
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
	if v, ok := env["HEALTH_ADDR"]; ok {
		cfg.HealthAddr = v
	}
	if v, ok := env["PUBSUB_EMULATOR_HOST"]; ok {
		cfg.EmulatorHost = v
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
	if visited["endpoint"] {
		cfg.Endpoint = f.endpoint
	}
	if visited["credentials"] {
		cfg.CredentialsFile = f.credentials
	}
	if visited["quota-project"] {
		cfg.QuotaProject = f.quota
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
	if visited["color"] {
		cfg.Color = f.color
	}
	if visited["max-outstanding-messages"] {
		cfg.MaxOutstandingMessages = f.maxMessages
	}
	if visited["max-outstanding-bytes"] {
		cfg.MaxOutstandingBytes = f.maxBytes
	}
	if visited["num-goroutines"] {
		cfg.NumGoroutines = f.goroutines
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
	if visited["health-addr"] {
		cfg.HealthAddr = f.health
	}
	if visited["emulator-host"] {
		cfg.EmulatorHost = f.emulator
	}
	return nil
}

func isHealthOff(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "-", "none", "off":
		return true
	default:
		return false
	}
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

func (c *Config) applyHealthDefault(port string, disabled bool) {
	if disabled || isHealthOff(c.HealthAddr) {
		c.HealthAddr = ""
		return
	}
	if strings.TrimSpace(c.HealthAddr) == "" && strings.TrimSpace(port) != "" {
		c.HealthAddr = port
	}
	c.HealthAddr = normalizeAddr(c.HealthAddr)
}

func normalizeAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" || isHealthOff(addr) {
		return ""
	}
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	if !strings.Contains(addr, ":") {
		return ":" + addr
	}
	return addr
}

func (c *Config) normalize() error {
	var err error
	c.ProjectID = strings.TrimSpace(c.ProjectID)
	c.Subscription = strings.TrimSpace(c.Subscription)
	c.CredentialsFile = strings.TrimSpace(c.CredentialsFile)
	c.QuotaProject = strings.TrimSpace(c.QuotaProject)
	c.EmulatorHost = strings.TrimSpace(c.EmulatorHost)
	c.Format = strings.ToLower(strings.TrimSpace(c.Format))
	c.Color = strings.ToLower(strings.TrimSpace(c.Color))
	if c.Format == "" {
		c.Format = defaultFormat
	}
	if c.Color == "" {
		c.Color = defaultColor
	}
	c.UniverseDomain, err = NormalizeUniverse(c.UniverseDomain)
	if err != nil {
		return err
	}
	c.Endpoint, err = NormalizeEndpoint(c.Endpoint)
	if err != nil {
		return err
	}
	if c.MaxOutstandingMessages == 0 {
		c.MaxOutstandingMessages = DefaultMaxOutstandingMessages
	}
	if c.MaxOutstandingBytes == 0 {
		c.MaxOutstandingBytes = DefaultMaxOutstandingBytes
	}
	if c.NumGoroutines == 0 {
		c.NumGoroutines = DefaultNumGoroutines
	}
	return nil
}

// Validate checks the resolved configuration.
func (c Config) Validate() error {
	var errs []error
	if c.ProjectID == "" {
		errs = append(errs, errors.New("project_id is required (flag --project, PUBSUB_PROJECT_ID, or config file)"))
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
	switch c.Color {
	case "auto", "always", "never":
	default:
		errs = append(errs, fmt.Errorf("color must be auto, always, or never, got %q", c.Color))
	}
	if c.MaxOutstandingMessages < 1 {
		errs = append(errs, fmt.Errorf("max_outstanding_messages must be >= 1, got %d", c.MaxOutstandingMessages))
	}
	if c.MaxOutstandingBytes < 1 {
		errs = append(errs, fmt.Errorf("max_outstanding_bytes must be >= 1, got %d", c.MaxOutstandingBytes))
	}
	if c.NumGoroutines < 1 {
		errs = append(errs, fmt.Errorf("num_goroutines must be >= 1, got %d", c.NumGoroutines))
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

// NormalizeEndpoint accepts a host, host:port, or https URL and returns host:port.
func NormalizeEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("endpoint: %w", err)
		}
		if u.Scheme != "https" && u.Scheme != "http" {
			return "", fmt.Errorf("endpoint scheme must be https, got %q", u.Scheme)
		}
		if u.Hostname() == "" {
			return "", fmt.Errorf("endpoint %q is missing a host", raw)
		}
		if p := strings.TrimPrefix(u.Path, "/"); p != "" || u.RawQuery != "" || u.Fragment != "" {
			return "", fmt.Errorf("endpoint must not include a path, got %q", raw)
		}
		port := u.Port()
		if port == "" {
			port = "443"
		}
		return u.Hostname() + ":" + port, nil
	}
	if strings.Contains(raw, "/") {
		return "", fmt.Errorf("endpoint must be host:port, got %q", raw)
	}
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		if strings.Contains(err.Error(), "missing port") {
			return raw + ":443", nil
		}
		return "", fmt.Errorf("endpoint: %w", err)
	}
	if host == "" || port == "" {
		return "", fmt.Errorf("endpoint %q is missing a host or port", raw)
	}
	return host + ":" + port, nil
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

// firstEnv returns the first listed key that is present, even when its value
// is empty. Presence matters so an explicit empty override can be detected
// by callers that need it; Load treats empty strings as values.
func firstEnv(env map[string]string, keys ...string) (string, bool) {
	for _, key := range keys {
		if v, ok := env[key]; ok {
			return v, true
		}
	}
	return "", false
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

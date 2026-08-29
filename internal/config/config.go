package config

import (
	"cmp"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type OIDC struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	Scopes       []string
}

func (o OIDC) Enabled() bool { return o.Issuer != "" }

type Config struct {
	Addr             string
	DataDir          string
	BaseURL          *url.URL
	PublishURL       *url.URL
	DatabaseDSN      string
	MasterKey        []byte
	OIDC             OIDC
	DemoMode         bool
	UnsafeSSH        bool
	DemoEmail        string
	MappingTargets   []netip.Prefix
	ExitTargets      []netip.Prefix
	TrustedProxies   []netip.Prefix
	AllowedDERPHosts []string
	SessionIdle      time.Duration
	SessionMax       time.Duration
}

func Load() (Config, error) {
	demo := envBool("TAILCAT_WEBUI_DEMO_MODE", false)
	dataDir := env("TAILCAT_WEBUI_DATA_DIR", "./data")
	baseURL, err := url.Parse(env("TAILCAT_WEBUI_BASE_URL", "http://localhost:8080"))
	if err != nil {
		return Config{}, fmt.Errorf("parse TAILCAT_WEBUI_BASE_URL: %w", err)
	}
	var publishURL *url.URL
	publishRaw := strings.TrimSpace(os.Getenv("TAILCAT_WEBUI_PUBLISH_BASE_URL"))
	if publishRaw == "" && demo {
		publishURL = defaultDemoPublishURL(baseURL)
	} else if publishRaw != "" {
		publishURL, err = url.Parse(publishRaw)
		if err != nil {
			return Config{}, fmt.Errorf("parse TAILCAT_WEBUI_PUBLISH_BASE_URL: %w", err)
		}
	}
	masterKey, err := decodeMasterKey(strings.TrimSpace(os.Getenv("TAILCAT_WEBUI_MASTER_KEY")))
	if err != nil {
		return Config{}, err
	}
	mappingTargets, err := parsePrefixes(env("TAILCAT_WEBUI_ALLOWED_MAPPING_TARGETS", env("TAILCAT_WEBUI_ALLOWED_TARGETS", "127.0.0.0/8,::1/128")))
	if err != nil {
		return Config{}, err
	}
	exitTargets, err := parsePrefixes(strings.TrimSpace(os.Getenv("TAILCAT_WEBUI_ALLOWED_EXIT_TARGETS")))
	if err != nil {
		return Config{}, err
	}
	trustedProxies, err := parsePrefixes(strings.TrimSpace(os.Getenv("TAILCAT_WEBUI_TRUSTED_PROXIES")))
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Addr:        env("TAILCAT_WEBUI_ADDR", "127.0.0.1:8080"),
		DataDir:     dataDir,
		BaseURL:     baseURL,
		PublishURL:  publishURL,
		DatabaseDSN: SQLiteDSN(filepath.Join(dataDir, "tailcat-webui.db")),
		MasterKey:   masterKey,
		OIDC: OIDC{
			Issuer:       strings.TrimSpace(os.Getenv("TAILCAT_WEBUI_OIDC_ISSUER")),
			ClientID:     strings.TrimSpace(os.Getenv("TAILCAT_WEBUI_OIDC_CLIENT_ID")),
			ClientSecret: os.Getenv("TAILCAT_WEBUI_OIDC_CLIENT_SECRET"),
			Scopes:       splitCSV(env("TAILCAT_WEBUI_OIDC_SCOPES", "openid,profile,email")),
		},
		DemoMode:         demo,
		UnsafeSSH:        envBool("TAILCAT_WEBUI_DEMO_UNSAFE_SSH", false),
		DemoEmail:        env("TAILCAT_WEBUI_DEMO_EMAIL", "operator@example.test"),
		MappingTargets:   mappingTargets,
		ExitTargets:      exitTargets,
		TrustedProxies:   trustedProxies,
		AllowedDERPHosts: splitCSV(strings.ToLower(os.Getenv("TAILCAT_WEBUI_ALLOWED_DERP_HOSTS"))),
		SessionIdle:      24 * time.Hour,
		SessionMax:       7 * 24 * time.Hour,
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.BaseURL == nil || c.BaseURL.Host == "" {
		return errors.New("TAILCAT_WEBUI_BASE_URL must be an absolute URL")
	}
	if c.BaseURL.User != nil || (c.BaseURL.Path != "" && c.BaseURL.Path != "/") || c.BaseURL.RawQuery != "" || c.BaseURL.Fragment != "" {
		return errors.New("TAILCAT_WEBUI_BASE_URL must be an origin without credentials, path, query, or fragment")
	}
	if c.BaseURL.Scheme != "https" && !isLoopbackHost(c.BaseURL.Hostname()) {
		return errors.New("TAILCAT_WEBUI_BASE_URL must use HTTPS outside loopback development")
	}
	if c.PublishURL == nil || c.PublishURL.Host == "" {
		return errors.New("TAILCAT_WEBUI_PUBLISH_BASE_URL is required and must use a separate origin")
	}
	if c.PublishURL.User != nil || (c.PublishURL.Path != "" && c.PublishURL.Path != "/") || c.PublishURL.RawQuery != "" || c.PublishURL.Fragment != "" {
		return errors.New("TAILCAT_WEBUI_PUBLISH_BASE_URL must be an origin without credentials, path, query, or fragment")
	}
	if c.PublishURL.Scheme != "https" && !isLoopbackHost(c.PublishURL.Hostname()) {
		return errors.New("TAILCAT_WEBUI_PUBLISH_BASE_URL must use HTTPS outside loopback development")
	}
	if strings.EqualFold(c.PublishURL.Scheme, c.BaseURL.Scheme) && strings.EqualFold(c.PublishURL.Host, c.BaseURL.Host) {
		return errors.New("TAILCAT_WEBUI_PUBLISH_BASE_URL must use a different origin from TAILCAT_WEBUI_BASE_URL")
	}
	if c.DemoMode {
		if !isLoopbackHost(c.BaseURL.Hostname()) || !isLoopbackAddr(c.Addr) {
			return errors.New("demo mode is restricted to a loopback URL and listen address")
		}
		return nil
	}
	if c.UnsafeSSH {
		return errors.New("TAILCAT_WEBUI_DEMO_UNSAFE_SSH is allowed only in loopback demo mode")
	}
	if !c.OIDC.Enabled() || c.OIDC.ClientID == "" || c.OIDC.ClientSecret == "" {
		return errors.New("OIDC issuer, client ID and client secret are required unless demo mode is enabled")
	}
	if len(c.MasterKey) != 32 {
		return errors.New("TAILCAT_WEBUI_MASTER_KEY is required outside demo mode")
	}
	return nil
}

func (c Config) SecureCookies() bool { return c.BaseURL.Scheme == "https" }

func (c Config) RedirectURL() string {
	return c.BaseURL.JoinPath("/api/v1/auth/callback").String()
}

func EnsureRuntimeDirs(c Config) error {
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	if err := os.Chmod(c.DataDir, 0o700); err != nil {
		return fmt.Errorf("secure data directory: %w", err)
	}
	return nil
}

func SecureRuntimeFiles(c Config) error {
	for _, name := range []string{"tailcat-webui.db", "tailcat-webui.db-wal", "tailcat-webui.db-shm", "tailcat-webui.lock"} {
		path := filepath.Join(c.DataDir, name)
		if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("secure runtime file %s: %w", name, err)
		}
	}
	return nil
}

func SQLiteDSN(path string) string {
	return fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=cache_size(-20000)&_pragma=temp_store(MEMORY)&_pragma=mmap_size(268435456)", path)
}

func env(key, fallback string) string {
	return cmp.Or(strings.TrimSpace(os.Getenv(key)), fallback)
}

func envBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	return cmp.Or(value, fallback && err != nil)
}

func splitCSV(raw string) []string {
	var values []string
	for item := range strings.SplitSeq(raw, ",") {
		if value := strings.TrimSpace(item); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func parsePrefixes(raw string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for item := range strings.SplitSeq(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(item)
		if err != nil {
			return nil, fmt.Errorf("parse allowed target %q: %w", item, err)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

func decodeMasterKey(raw string) ([]byte, error) {
	if raw == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode TAILCAT_WEBUI_MASTER_KEY: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("TAILCAT_WEBUI_MASTER_KEY must decode to 32 bytes, got %d", len(key))
	}
	return key, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

func defaultDemoPublishURL(base *url.URL) *url.URL {
	host := "publish.localhost"
	if port := base.Port(); port != "" {
		host = net.JoinHostPort(host, port)
	}
	return &url.URL{Scheme: base.Scheme, Host: host}
}

func isLoopbackAddr(raw string) bool {
	host, _, ok := strings.CutLast(raw, ":")
	if !ok {
		return false
	}
	host = strings.Trim(host, "[]")
	return isLoopbackHost(host)
}

// Package config loads netward's configuration from a YAML file.
//
// Every setting has a default (see [Default]), so a configuration file only
// needs the values that differ. Keys that are left out keep their default;
// an empty string turns off an optional address, endpoint or target. Unknown
// keys are rejected so that typos do not go unnoticed.
//
// Durations are written like "30s", "1m" or "1h30m".
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path"
	"strconv"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/devusSs/netward/internal/domain/domainip"
)

// LogFormat is the output format of the logs.
type LogFormat string

const (
	// LogFormatText writes human-readable key=value lines.
	LogFormatText LogFormat = "text"
	// LogFormatJSON writes one JSON object per line.
	LogFormatJSON LogFormat = "json"
)

// Config is netward's configuration.
type Config struct {
	// Database is the path of the SQLite database file.
	Database string `yaml:"database"`
	// Log configures logging.
	Log Log `yaml:"log"`
	// Ping configures the echo requests of the public connectivity and the
	// gateway checks.
	Ping Ping `yaml:"ping"`
	// PublicNet configures the public address and connectivity checks.
	PublicNet PublicNet `yaml:"publicnet"`
	// PrivateNet configures the private address and gateway checks.
	PrivateNet PrivateNet `yaml:"privatenet"`
	// DomainIP configures domain IP monitoring.
	DomainIP DomainIP `yaml:"domainip"`
	// HTTP configures the web interface and its API.
	HTTP HTTP `yaml:"http"`
}

// HTTP configures the web interface and its API.
type HTTP struct {
	// Enabled turns the web interface on.
	Enabled bool `yaml:"enabled"`
	// Address is the address to listen on, for example ":8443" or
	// "192.168.178.26:443".
	Address string `yaml:"address"`
	// TLS configures HTTPS.
	TLS TLS `yaml:"tls"`
}

// TLS configures HTTPS. When the certificate and key files do not exist,
// netward creates a self-signed certificate at those paths and reuses it on
// later starts.
type TLS struct {
	// Enabled serves HTTPS instead of plain HTTP.
	Enabled bool `yaml:"enabled"`
	// Cert is the path of the PEM-encoded certificate (chain).
	Cert string `yaml:"cert"`
	// Key is the path of the PEM-encoded private key.
	Key string `yaml:"key"`
	// Hosts are extra host names or IP addresses for a self-signed
	// certificate, for example "netward.home". The host name, localhost and
	// the host's addresses are always included.
	Hosts []string `yaml:"hosts"`
}

// Log configures logging.
type Log struct {
	// Level is the minimum level logged: debug, info, warn or error.
	Level slog.Level `yaml:"level"`
	// Format is text or json.
	Format LogFormat `yaml:"format"`
}

// Ping configures echo requests.
type Ping struct {
	// Count is the number of echo requests per check.
	Count int `yaml:"count"`
	// Interval is the wait between echo requests.
	Interval time.Duration `yaml:"interval"`
	// Timeout limits a whole check.
	Timeout time.Duration `yaml:"timeout"`
	// Privileged sends raw ICMP, which needs root or CAP_NET_RAW, instead of
	// unprivileged pings.
	Privileged bool `yaml:"privileged"`
}

// PublicNet configures the public address and connectivity checks.
type PublicNet struct {
	// Enabled turns the checks on.
	Enabled bool `yaml:"enabled"`
	// Interval is the time between two checks.
	Interval time.Duration `yaml:"interval"`
	// Lookup configures the public address lookups.
	Lookup Lookup `yaml:"lookup"`
	// Targets are the servers pinged to check connectivity.
	Targets Targets `yaml:"targets"`
}

// Lookup configures the public address lookups.
type Lookup struct {
	// Timeout limits each lookup.
	Timeout time.Duration `yaml:"timeout"`
	// IPv4Endpoint is the URL that returns the public IPv4 address. Empty
	// turns the IPv4 lookup off.
	IPv4Endpoint string `yaml:"ipv4_endpoint"`
	// IPv6Endpoint is the URL that returns the public IPv6 address. Empty
	// turns the IPv6 lookup off.
	IPv6Endpoint string `yaml:"ipv6_endpoint"`
}

// Targets are the servers pinged to check connectivity. An empty target turns
// the check of its family off.
type Targets struct {
	IPv4 netip.Addr `yaml:"ipv4"`
	IPv6 netip.Addr `yaml:"ipv6"`
}

// PrivateNet configures the private address and gateway checks.
type PrivateNet struct {
	// Enabled turns the checks on.
	Enabled bool `yaml:"enabled"`
	// Interval is the time between two checks.
	Interval time.Duration `yaml:"interval"`
	// Gateways turns the default gateway checks on.
	Gateways bool `yaml:"gateways"`
	// Interfaces selects the monitored network interfaces.
	Interfaces Interfaces `yaml:"interfaces"`
}

// Interfaces selects network interfaces by name with glob patterns such as
// "en*" or "docker*".
type Interfaces struct {
	// Include lists the interfaces to monitor. Empty means all.
	Include []string `yaml:"include"`
	// Exclude lists interfaces to skip, even if they are included.
	Exclude []string `yaml:"exclude"`
}

// DomainIP configures domain IP monitoring.
type DomainIP struct {
	// Enabled turns the checks on. It requires at least one domain.
	Enabled bool `yaml:"enabled"`
	// Interval is the time between two checks.
	Interval time.Duration `yaml:"interval"`
	// Resolver is the DNS server to query, for example "1.1.1.1" or
	// "192.168.178.1:53". Empty uses the system resolver.
	Resolver string `yaml:"resolver"`
	// Timeout limits each lookup.
	Timeout time.Duration `yaml:"timeout"`
	// Domains are the domains to monitor.
	Domains []string `yaml:"domains"`
}

// Default returns the default configuration: public and private net checks
// every minute, domain IP monitoring off, text logs at info level and the web
// interface on HTTPS port 8443 with a self-signed certificate.
func Default() Config {
	const (
		defaultInterval       = time.Minute
		defaultDomainInterval = 5 * time.Minute
		defaultTimeout        = 5 * time.Second
		defaultPingCount      = 4
	)

	return Config{
		Database: "netward.db",
		Log: Log{
			Level:  slog.LevelInfo,
			Format: LogFormatText,
		},
		Ping: Ping{
			Count:      defaultPingCount,
			Interval:   time.Second,
			Timeout:    defaultTimeout,
			Privileged: false,
		},
		PublicNet: PublicNet{
			Enabled:  true,
			Interval: defaultInterval,
			Lookup: Lookup{
				Timeout:      defaultTimeout,
				IPv4Endpoint: "https://api.ipify.org",
				IPv6Endpoint: "https://api6.ipify.org",
			},
			Targets: Targets{
				IPv4: netip.MustParseAddr("1.1.1.1"),
				IPv6: netip.MustParseAddr("2606:4700:4700::1111"),
			},
		},
		PrivateNet: PrivateNet{
			Enabled:  true,
			Interval: defaultInterval,
			Gateways: true,
			Interfaces: Interfaces{
				Include: nil,
				Exclude: nil,
			},
		},
		DomainIP: DomainIP{
			Enabled:  false,
			Interval: defaultDomainInterval,
			Resolver: "",
			Timeout:  defaultTimeout,
			Domains:  nil,
		},
		HTTP: HTTP{
			Enabled: true,
			Address: ":8443",
			TLS: TLS{
				Enabled: true,
				Cert:    "netward-cert.pem",
				Key:     "netward-key.pem",
				Hosts:   nil,
			},
		},
	}
}

// Load reads the configuration file at path, applies it on top of [Default]
// and validates the result.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	cfg, err := Parse(data)
	if err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}

	return cfg, nil
}

// Parse applies the YAML document in data on top of [Default] and validates
// the result. An empty document yields the defaults.
func Parse(data []byte) (Config, error) {
	cfg := Default()

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	err := decoder.Decode(&cfg)
	if err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("parse: %w", err)
	}

	err = cfg.Validate()
	if err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// Validate reports every invalid setting, each prefixed with its YAML key.
//
// It checks values the configuration itself defines. Endpoint URLs, the DNS
// server address and the ping limits are checked by the components that use
// them when they are created.
func (c Config) Validate() error {
	var errs []error

	check := func(ok bool, key, format string, args ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf("%s: "+format, append([]any{key}, args...)...))
		}
	}

	check(c.Database != "", "database", "must not be empty")
	check(c.Log.Format == LogFormatText || c.Log.Format == LogFormatJSON,
		"log.format", "must be text or json, got %q", c.Log.Format)
	check(c.PublicNet.Enabled || c.PrivateNet.Enabled || c.DomainIP.Enabled,
		"publicnet, privatenet, domainip", "at least one must be enabled")

	check(c.Ping.Count > 0, "ping.count", "must be positive, got %d", c.Ping.Count)
	check(c.Ping.Interval > 0, "ping.interval", "must be positive, got %s", c.Ping.Interval)
	check(c.Ping.Timeout > 0, "ping.timeout", "must be positive, got %s", c.Ping.Timeout)

	check(c.PublicNet.Interval > 0, "publicnet.interval", "must be positive, got %s", c.PublicNet.Interval)
	check(c.PublicNet.Lookup.Timeout > 0, "publicnet.lookup.timeout", "must be positive, got %s",
		c.PublicNet.Lookup.Timeout)
	check(isZeroOr(c.PublicNet.Targets.IPv4, netip.Addr.Is4), "publicnet.targets.ipv4",
		"%s is not an IPv4 address", c.PublicNet.Targets.IPv4)
	check(isZeroOr(c.PublicNet.Targets.IPv6, netip.Addr.Is6), "publicnet.targets.ipv6",
		"%s is not an IPv6 address", c.PublicNet.Targets.IPv6)

	check(c.PrivateNet.Interval > 0, "privatenet.interval", "must be positive, got %s", c.PrivateNet.Interval)
	errs = append(errs, validatePatterns("privatenet.interfaces.include", c.PrivateNet.Interfaces.Include)...)
	errs = append(errs, validatePatterns("privatenet.interfaces.exclude", c.PrivateNet.Interfaces.Exclude)...)

	check(c.DomainIP.Interval > 0, "domainip.interval", "must be positive, got %s", c.DomainIP.Interval)
	check(c.DomainIP.Timeout > 0, "domainip.timeout", "must be positive, got %s", c.DomainIP.Timeout)
	check(!c.DomainIP.Enabled || len(c.DomainIP.Domains) > 0, "domainip.domains",
		"at least one domain is required when domainip is enabled")

	for i, d := range c.DomainIP.Domains {
		_, err := domainip.NormalizeDomain(d)
		check(err == nil, fmt.Sprintf("domainip.domains[%d]", i), "%v", err)
	}

	errs = append(errs, c.HTTP.validate()...)

	return errors.Join(errs...)
}

func (h HTTP) validate() []error {
	var errs []error

	_, port, err := net.SplitHostPort(h.Address)
	if err == nil {
		_, err = strconv.ParseUint(port, 10, 16)
	}

	if err != nil {
		errs = append(errs, fmt.Errorf("http.address: %q must be host:port or :port: %w", h.Address, err))
	}

	if !h.TLS.Enabled {
		return errs
	}

	if h.TLS.Cert == "" {
		errs = append(errs, errors.New("http.tls.cert: must not be empty when tls is enabled"))
	}

	if h.TLS.Key == "" {
		errs = append(errs, errors.New("http.tls.key: must not be empty when tls is enabled"))
	}

	for i, host := range h.TLS.Hosts {
		_, addrErr := netip.ParseAddr(host)
		if addrErr == nil {
			continue
		}

		_, nameErr := domainip.NormalizeDomain(host)
		if nameErr != nil {
			errs = append(errs, fmt.Errorf("http.tls.hosts[%d]: %q is neither an IP address nor a host name", i, host))
		}
	}

	return errs
}

func isZeroOr(addr netip.Addr, inFamily func(netip.Addr) bool) bool {
	return !addr.IsValid() || inFamily(addr.Unmap())
}

func validatePatterns(key string, patterns []string) []error {
	var errs []error

	for i, pattern := range patterns {
		_, err := path.Match(pattern, "")
		if err != nil {
			errs = append(errs, fmt.Errorf("%s[%d]: pattern %q: %w", key, i, pattern, err))
		}
	}

	return errs
}

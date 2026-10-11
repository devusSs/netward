package config_test

import (
	"errors"
	"io/fs"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devusSs/netward/internal/config"
)

func TestDefaultIsValid(t *testing.T) {
	t.Parallel()

	if err := config.Default().Validate(); err != nil {
		t.Errorf("Default().Validate() error = %v", err)
	}
}

func TestParseEmpty(t *testing.T) {
	t.Parallel()

	for _, doc := range []string{"", "\n", "# only a comment\n", "---\n"} {
		cfg, err := config.Parse([]byte(doc))
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", doc, err)
		}

		if !reflect.DeepEqual(cfg, config.Default()) {
			t.Errorf("Parse(%q) = %+v, want the defaults", doc, cfg)
		}
	}
}

// TestExampleMatchesDefaults keeps netward.example.yaml in sync with Default:
// the example documents every default, so parsing it must change nothing.
func TestExampleMatchesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(filepath.Join("..", "..", "netward.example.yaml"))
	if err != nil {
		t.Fatalf("Load(example) error = %v", err)
	}

	if !reflect.DeepEqual(cfg, config.Default()) {
		t.Errorf("example config = %+v\nwant the defaults %+v", cfg, config.Default())
	}
}

func TestParseOverrides(t *testing.T) {
	t.Parallel()

	cfg, err := config.Parse([]byte(`
database: /var/lib/netward/netward.db
retention: 168h
log:
  level: DEBUG
  format: json
ping:
  count: 3
  interval: 200ms
  privileged: true
publicnet:
  interval: 30s
  lookup:
    ipv6_endpoint: ""
  targets:
    ipv4: 9.9.9.9
    ipv6: ""
privatenet:
  enabled: false
  interfaces:
    exclude: ["docker*", "utun*"]
domainip:
  enabled: true
  resolver: 192.168.178.1
  domains:
    - home.example.com
    - nas.example.com
http:
  address: 127.0.0.1:443
  tls:
    hosts: [netward.home, 192.168.178.26]
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	want := config.Default()
	want.Database = "/var/lib/netward/netward.db"
	want.Retention = 7 * 24 * time.Hour
	want.Log = config.Log{Level: slog.LevelDebug, Format: config.LogFormatJSON}
	want.Ping.Count = 3
	want.Ping.Interval = 200 * time.Millisecond
	want.Ping.Privileged = true
	want.PublicNet.Interval = 30 * time.Second
	want.PublicNet.Lookup.IPv6Endpoint = ""
	want.PublicNet.Targets = config.Targets{IPv4: netip.MustParseAddr("9.9.9.9"), IPv6: netip.Addr{}}
	want.PrivateNet.Enabled = false
	want.PrivateNet.Interfaces.Exclude = []string{"docker*", "utun*"}
	want.DomainIP.Enabled = true
	want.DomainIP.Resolver = "192.168.178.1"
	want.DomainIP.Domains = []string{"home.example.com", "nas.example.com"}
	want.HTTP.Address = "127.0.0.1:443"
	want.HTTP.TLS.Hosts = []string{"netward.home", "192.168.178.26"}

	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Parse() = %+v\nwant %+v", cfg, want)
	}
}

func TestParseRejectsBadYAML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		doc     string
		wantMsg string
	}{
		{"unknown key", "publicnet:\n  intervall: 1m\n", "field intervall not found"},
		{"wrong type", "ping:\n  count: four\n", "cannot unmarshal"},
		{"bad duration", "publicnet:\n  interval: 5 minutes\n", "cannot unmarshal"},
		{"bad address", "publicnet:\n  targets:\n    ipv4: 1.1.1\n", "ParseAddr"},
		{"bad log level", "log:\n  level: verbose\n", "verbose"},
		{"not a mapping", "- a\n- b\n", "cannot unmarshal"},
		{"syntax error", "log: [\n", "parse"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := config.Parse([]byte(tt.doc))
			if err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("Parse() error = %v, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		modify  func(cfg *config.Config)
		wantMsg string
	}{
		{"empty database", func(c *config.Config) { c.Database = "" }, "database: must not be empty"},
		{"negative retention", func(c *config.Config) { c.Retention = -time.Hour },
			"retention: must not be negative, got -1h0m0s"},
		{"bad log format", func(c *config.Config) { c.Log.Format = "xml" },
			`log.format: must be text or json, got "xml"`},
		{"everything disabled", func(c *config.Config) {
			c.PublicNet.Enabled = false
			c.PrivateNet.Enabled = false
		}, "publicnet, privatenet, domainip: at least one must be enabled"},
		{"zero ping count", func(c *config.Config) { c.Ping.Count = 0 }, "ping.count: must be positive, got 0"},
		{"zero ping interval", func(c *config.Config) { c.Ping.Interval = 0 }, "ping.interval: must be positive"},
		{"negative ping timeout", func(c *config.Config) { c.Ping.Timeout = -time.Second },
			"ping.timeout: must be positive, got -1s"},
		{"zero publicnet interval", func(c *config.Config) { c.PublicNet.Interval = 0 },
			"publicnet.interval: must be positive"},
		{"zero lookup timeout", func(c *config.Config) { c.PublicNet.Lookup.Timeout = 0 },
			"publicnet.lookup.timeout: must be positive"},
		{"ipv6 as ipv4 target", func(c *config.Config) { c.PublicNet.Targets.IPv4 = netip.MustParseAddr("::1") },
			"publicnet.targets.ipv4: ::1 is not an IPv4 address"},
		{"ipv4 as ipv6 target", func(c *config.Config) {
			c.PublicNet.Targets.IPv6 = netip.MustParseAddr("::ffff:1.1.1.1")
		}, "publicnet.targets.ipv6: ::ffff:1.1.1.1 is not an IPv6 address"},
		{"zero privatenet interval", func(c *config.Config) { c.PrivateNet.Interval = 0 },
			"privatenet.interval: must be positive"},
		{"bad include pattern", func(c *config.Config) { c.PrivateNet.Interfaces.Include = []string{"en*", "en["} },
			`privatenet.interfaces.include[1]: pattern "en["`},
		{"bad exclude pattern", func(c *config.Config) { c.PrivateNet.Interfaces.Exclude = []string{"["} },
			`privatenet.interfaces.exclude[0]: pattern "["`},
		{"zero domainip interval", func(c *config.Config) { c.DomainIP.Interval = 0 },
			"domainip.interval: must be positive"},
		{"zero domainip timeout", func(c *config.Config) { c.DomainIP.Timeout = 0 },
			"domainip.timeout: must be positive"},
		{"domainip without domains", func(c *config.Config) { c.DomainIP.Enabled = true },
			"domainip.domains: at least one domain is required when domainip is enabled"},
		{"invalid domain", func(c *config.Config) { c.DomainIP.Domains = []string{"ok.example", "https://x.example"} },
			`domainip.domains[1]: domain "https://x.example": invalid character ':'`},
		{"address without port", func(c *config.Config) { c.HTTP.Address = "localhost" },
			`http.address: "localhost" must be host:port or :port`},
		{"address with bad port", func(c *config.Config) { c.HTTP.Address = ":99999" },
			`http.address: ":99999" must be host:port or :port`},
		{"empty cert", func(c *config.Config) { c.HTTP.TLS.Cert = "" },
			"http.tls.cert: must not be empty when tls is enabled"},
		{"empty key", func(c *config.Config) { c.HTTP.TLS.Key = "" },
			"http.tls.key: must not be empty when tls is enabled"},
		{"bad tls host", func(c *config.Config) { c.HTTP.TLS.Hosts = []string{"netward.home", "not a host"} },
			`http.tls.hosts[1]: "not a host" is neither an IP address nor a host name`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.Default()
			tt.modify(&cfg)

			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("Validate() error = %v, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestValidateAcceptsOptionalValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		modify func(cfg *config.Config)
	}{
		{"only domainip enabled", func(c *config.Config) {
			c.PublicNet.Enabled = false
			c.PrivateNet.Enabled = false
			c.DomainIP.Enabled = true
			c.DomainIP.Domains = []string{"home.example.com"}
		}},
		{
			"no ping targets",
			func(c *config.Config) { c.PublicNet.Targets = config.Targets{IPv4: netip.Addr{}, IPv6: netip.Addr{}} },
		},
		{"no tls files without tls", func(c *config.Config) {
			c.HTTP.TLS.Enabled = false
			c.HTTP.TLS.Cert = ""
			c.HTTP.TLS.Key = ""
		}},
		{"ip and name tls hosts", func(c *config.Config) {
			c.HTTP.TLS.Hosts = []string{"netward.home", "192.168.178.26", "fd00::26"}
		}},
		{"keep snapshots forever", func(c *config.Config) { c.Retention = 0 }},
		{"web interface off", func(c *config.Config) {
			c.HTTP.Enabled = false
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.Default()
			tt.modify(&cfg)

			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() error = %v", err)
			}
		})
	}
}

func TestValidateJoinsErrors(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Database = ""
	cfg.Ping.Count = 0
	cfg.HTTP.Address = "nope"

	err := cfg.Validate()
	for _, want := range []string{"database:", "ping.count:", "http.address:"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error = %v, want it to contain %q", err, want)
		}
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "netward.yaml")
	if err := os.WriteFile(path, []byte("publicnet:\n  interval: 2m\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.PublicNet.Interval != 2*time.Minute {
		t.Errorf("PublicNet.Interval = %s, want 2m", cfg.PublicNet.Interval)
	}
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	invalid := filepath.Join(dir, "invalid.yaml")
	if err := os.WriteFile(invalid, []byte("ping:\n  count: 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := config.Load(filepath.Join(dir, "missing.yaml"))
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "read config") {
		t.Errorf("Load(missing) error = %v, want a not-exist error", err)
	}

	_, err = config.Load(invalid)
	if err == nil || !strings.Contains(err.Error(), "config "+invalid+": ping.count") {
		t.Errorf("Load(invalid) error = %v, want it to name the file and key", err)
	}
}

func TestDefaultReturnsFreshValues(t *testing.T) {
	t.Parallel()

	a := config.Default()
	a.HTTP.TLS.Hosts = append(a.HTTP.TLS.Hosts, "changed")

	if slices.Contains(config.Default().HTTP.TLS.Hosts, "changed") {
		t.Error("changing one Default() result changed another")
	}
}

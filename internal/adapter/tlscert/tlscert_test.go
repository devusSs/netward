package tlscert_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devusSs/netward/internal/adapter/tlscert"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock {
	return &clock{mu: sync.Mutex{}, now: time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

func ipNet(t *testing.T, cidr string) net.Addr {
	t.Helper()

	ip, network, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}

	return &net.IPNet{IP: ip, Mask: network.Mask}
}

type env struct {
	cfg      tlscert.Config
	clock    *clock
	hostname string
	addrs    []net.Addr
	addrsErr error
	logs     *bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Helper()

	dir := t.TempDir()
	logs := &bytes.Buffer{}

	return &env{
		cfg: tlscert.Config{
			CertFile: filepath.Join(dir, "netward-cert.pem"),
			KeyFile:  filepath.Join(dir, "netward-key.pem"),
			Hosts:    []string{"netward.home", "10.0.0.26"},
			Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{
				AddSource:   false,
				Level:       slog.LevelDebug,
				ReplaceAttr: nil,
			})),
		},
		clock:    newClock(),
		hostname: "Pi-Netward",
		addrs: []net.Addr{
			ipNet(t, "127.0.0.1/8"),
			ipNet(t, "192.168.178.26/24"),
			ipNet(t, "fd00::26/64"),
			ipNet(t, "fe80::26/64"),
			ipNet(t, "2001:db8::26/64"),
			&net.IPAddr{IP: net.ParseIP("192.168.178.27"), Zone: ""},
		},
		addrsErr: nil,
		logs:     logs,
	}
}

func (e *env) manager(t *testing.T) *tlscert.Manager {
	t.Helper()

	m, err := e.newManager()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return m
}

func (e *env) newManager() (*tlscert.Manager, error) {
	return tlscert.NewWithDeps(
		e.cfg,
		e.clock.Now,
		func() (string, error) { return e.hostname, nil },
		func() ([]net.Addr, error) { return e.addrs, e.addrsErr },
	)
}

func leaf(t *testing.T, m *tlscert.Manager) *x509.Certificate {
	t.Helper()

	cert, err := m.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate() error = %v", err)
	}

	return cert.Leaf
}

func ipStrings(ips []net.IP) []string {
	result := make([]string, 0, len(ips))
	for _, ip := range ips {
		result = append(result, ip.String())
	}

	return result
}

func TestNewGeneratesCertificate(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	m := e.manager(t)
	cert := leaf(t, m)

	if !m.Generated() {
		t.Error("Generated() = false, want true")
	}

	if want := []string{"localhost", "netward.home", "pi-netward"}; !slices.Equal(cert.DNSNames, want) {
		t.Errorf("DNSNames = %v, want %v", cert.DNSNames, want)
	}

	wantIPs := []string{"10.0.0.26", "127.0.0.1", "192.168.178.26", "::1", "fd00::26"}
	if got := ipStrings(cert.IPAddresses); !slices.Equal(got, wantIPs) {
		t.Errorf("IPAddresses = %v, want %v (no public, link-local or mask-less addresses)", got, wantIPs)
	}

	if !cert.NotBefore.Equal(e.clock.Now().Add(-time.Hour)) ||
		!cert.NotAfter.Equal(e.clock.Now().Add(397*24*time.Hour)) {
		t.Errorf("validity = %v to %v, want one hour back to 397 days ahead", cert.NotBefore, cert.NotAfter)
	}

	if cert.Subject.CommonName != "netward self-signed certificate" ||
		cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) != nil {
		t.Errorf("certificate = %v, want netward's self-signed certificate", cert.Subject)
	}

	if !strings.Contains(e.logs.String(), `msg="self-signed tls certificate generated" component=tlscert `+
		`reason="no certificate yet"`) {
		t.Errorf("logs = %s, want a generation log", e.logs.String())
	}
}

func TestNewWritesFiles(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("file modes are not enforced on Windows")
	}

	e := newEnv(t)
	e.manager(t)

	for path, want := range map[string]os.FileMode{e.cfg.CertFile: 0o644, e.cfg.KeyFile: 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}

		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", filepath.Base(path), info.Mode().Perm(), want)
		}
	}

	entries, err := os.ReadDir(filepath.Dir(e.cfg.CertFile))
	if err != nil || len(entries) != 2 {
		t.Errorf("directory has %d entries (%v), want only the certificate and key", len(entries), err)
	}
}

func TestNewReusesGeneratedCertificate(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	first := leaf(t, e.manager(t))

	e.clock.Advance(300 * 24 * time.Hour)
	e.logs.Reset()

	second := leaf(t, e.manager(t))

	if first.SerialNumber.Cmp(second.SerialNumber) != 0 {
		t.Error("certificate was regenerated, want it reused")
	}

	if !strings.Contains(e.logs.String(), `msg="tls certificate loaded" component=tlscert`) ||
		!strings.Contains(e.logs.String(), "self_signed=true") {
		t.Errorf("logs = %s, want a load log", e.logs.String())
	}
}

func TestNewRenewsGeneratedCertificate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		change     func(e *env)
		wantReason string
	}{
		{
			name:       "expires soon",
			change:     func(e *env) { e.clock.Advance(370 * 24 * time.Hour) },
			wantReason: "certificate expires soon",
		},
		{
			name: "new private address",
			change: func(e *env) {
				e.addrs = append(e.addrs, &net.IPNet{IP: net.ParseIP("10.8.0.2"), Mask: net.CIDRMask(24, 32)})
			},
			wantReason: "certificate does not cover 10.8.0.2",
		},
		{
			name:       "new host name",
			change:     func(e *env) { e.hostname = "netward-pi" },
			wantReason: "certificate does not cover netward-pi",
		},
		{
			name:       "new configured host",
			change:     func(e *env) { e.cfg.Hosts = append(e.cfg.Hosts, "netward.lan") },
			wantReason: "certificate does not cover netward.lan",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t)
			first := leaf(t, e.manager(t))

			tt.change(e)
			e.logs.Reset()

			second := leaf(t, e.manager(t))

			if first.SerialNumber.Cmp(second.SerialNumber) == 0 {
				t.Error("certificate was reused, want a new one")
			}

			if !strings.Contains(e.logs.String(), `reason="`+tt.wantReason+`"`) {
				t.Errorf("logs = %s, want reason %q", e.logs.String(), tt.wantReason)
			}
		})
	}
}

func TestGetCertificateRenewsWhileRunning(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	m := e.manager(t)
	first := leaf(t, m)

	e.clock.Advance(100 * 24 * time.Hour)

	if leaf(t, m).SerialNumber.Cmp(first.SerialNumber) != 0 {
		t.Fatal("certificate was replaced before it was due")
	}

	e.clock.Advance(280 * 24 * time.Hour)

	renewed := leaf(t, m)
	if renewed.SerialNumber.Cmp(first.SerialNumber) == 0 {
		t.Fatal("certificate was not renewed")
	}

	// The renewed certificate was written, so the next start uses it.
	if leaf(t, e.manager(t)).SerialNumber.Cmp(renewed.SerialNumber) != 0 {
		t.Error("renewed certificate was not saved")
	}
}

func TestGetCertificateKeepsOldCertificateWhenRenewalFails(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the test cannot write to")
	}

	e := newEnv(t)
	m := e.manager(t)
	first := leaf(t, m)

	dir := filepath.Dir(e.cfg.CertFile)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	e.clock.Advance(370 * 24 * time.Hour)

	if leaf(t, m).SerialNumber.Cmp(first.SerialNumber) != 0 {
		t.Error("certificate changed although renewal failed")
	}

	if !strings.Contains(e.logs.String(), `level=ERROR msg="renewing tls certificate failed"`) {
		t.Errorf("logs = %s, want a renewal error", e.logs.String())
	}
}

// writeUserCertificate writes a self-signed certificate that netward did not
// generate, valid until notAfter.
func writeUserCertificate(t *testing.T, e *env, notAfter time.Time) *big.Int {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	var subject pkix.Name
	subject.CommonName = "netward.home"

	var template x509.Certificate
	template.SerialNumber = big.NewInt(4242)
	template.Subject = subject
	template.NotBefore = e.clock.Now().Add(-48 * time.Hour)
	template.NotAfter = notAfter
	template.DNSNames = []string{"netward.home"}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Headers: nil, Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Headers: nil, Bytes: keyDER})

	if err = os.WriteFile(e.cfg.CertFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	if err = os.WriteFile(e.cfg.KeyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	return template.SerialNumber
}

func TestNewUsesUserCertificate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		validFor time.Duration
		wantLog  string
	}{
		{"valid", 200 * 24 * time.Hour, `level=INFO msg="tls certificate loaded"`},
		{"expires soon", 10 * 24 * time.Hour, `level=WARN msg="tls certificate expires soon"`},
		{"expired", -time.Hour, `level=WARN msg="tls certificate has expired"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t)
			serial := writeUserCertificate(t, e, e.clock.Now().Add(tt.validFor))

			m := e.manager(t)

			if m.Generated() || leaf(t, m).SerialNumber.Cmp(serial) != 0 {
				t.Error("user certificate was replaced or not loaded")
			}

			// Even far in the future the user's certificate stays untouched.
			e.clock.Advance(400 * 24 * time.Hour)

			if leaf(t, m).SerialNumber.Cmp(serial) != 0 {
				t.Error("user certificate was renewed")
			}

			if !strings.Contains(e.logs.String(), tt.wantLog) ||
				!strings.Contains(e.logs.String(), "self_signed=false") {
				t.Errorf("logs = %s, want %s", e.logs.String(), tt.wantLog)
			}
		})
	}
}

func TestNewErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare func(t *testing.T, e *env)
		wantMsg string
	}{
		{
			name:    "missing paths",
			prepare: func(_ *testing.T, e *env) { e.cfg.KeyFile = "" },
			wantMsg: "certificate and key file are required",
		},
		{
			name: "only the certificate exists",
			prepare: func(t *testing.T, e *env) {
				t.Helper()

				if err := os.WriteFile(e.cfg.CertFile, []byte("cert"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantMsg: "found only one of",
		},
		{
			name: "invalid files",
			prepare: func(t *testing.T, e *env) {
				t.Helper()

				for _, path := range []string{e.cfg.CertFile, e.cfg.KeyFile} {
					if err := os.WriteFile(path, []byte("not pem"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			},
			wantMsg: "load tls certificate",
		},
		{
			name:    "directory missing",
			prepare: func(_ *testing.T, e *env) { e.cfg.KeyFile = filepath.Join(e.cfg.KeyFile, "missing", "key.pem") },
			wantMsg: "write ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t)
			tt.prepare(t, e)

			m, err := e.newManager()
			if m != nil || err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("New() = %v, %v, want nil and an error containing %q", m, err, tt.wantMsg)
			}
		})
	}
}

func TestNewToleratesAddressErrors(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	e.addrs = nil
	e.addrsErr = errors.New("no interfaces")

	cert := leaf(t, e.manager(t))

	if want := []string{"10.0.0.26", "127.0.0.1", "::1"}; !slices.Equal(ipStrings(cert.IPAddresses), want) {
		t.Errorf("IPAddresses = %v, want %v", ipStrings(cert.IPAddresses), want)
	}

	if !strings.Contains(e.logs.String(), `level=WARN msg="listing interface addresses for tls certificate failed"`) {
		t.Errorf("logs = %s, want a warning", e.logs.String())
	}
}

func TestNewWithSystemDefaults(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	m, err := tlscert.New(tlscert.Config{
		CertFile: filepath.Join(dir, "cert.pem"),
		KeyFile:  filepath.Join(dir, "key.pem"),
		Hosts:    nil,
		Logger:   nil,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if cert := leaf(t, m); !slices.Contains(cert.DNSNames, "localhost") {
		t.Errorf("DNSNames = %v, want localhost", cert.DNSNames)
	}
}

func TestHandshake(t *testing.T) {
	t.Parallel()

	// A real client checks the validity against the real clock.
	e := newEnv(t)
	e.clock = &clock{mu: sync.Mutex{}, now: time.Now()}
	m := e.manager(t)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	srv.TLS = &tls.Config{
		GetCertificate: m.GetCertificate,
		MinVersion:     tls.VersionTLS12,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	roots := x509.NewCertPool()
	roots.AddCert(leaf(t, m))

	client := srv.Client()
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{ //nolint:forcetypeassert // httptest uses an *http.Transport.
		RootCAs:    roots,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS12,
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request over TLS: %v", err)
	}

	_ = resp.Body.Close()
}

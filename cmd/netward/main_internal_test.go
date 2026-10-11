package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devusSs/netward/internal/config"
)

func TestRunUsageAndConfigErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	invalid := filepath.Join(dir, "invalid.yaml")
	if err := os.WriteFile(invalid, []byte("publicnet:\n  intervall: 1m\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{"help", []string{"-h"}, 0, "-config string"},
		{"unknown flag", []string{"-verbose"}, 2, "flag provided but not defined: -verbose"},
		{"explicit config missing", []string{"-config", filepath.Join(dir, "missing.yaml")}, 1,
			"netward: read config: open "},
		{"invalid config", []string{"-config", invalid}, 1, "field intervall not found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			if code := run(tt.args, &out); code != tt.wantCode || !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf(
					"run(%v) = %d, output %q, want %d and %q",
					tt.args,
					code,
					out.String(),
					tt.wantCode,
					tt.wantOut,
				)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	missing := filepath.Join(dir, "netward.yaml")

	cfg, source, err := loadConfig(missing, false)
	if err != nil || !reflect.DeepEqual(cfg, config.Default()) || source != "defaults (no "+missing+")" {
		t.Errorf("loadConfig(missing, implicit) = %v, %q, want the defaults", err, source)
	}

	if _, _, err = loadConfig(missing, true); err == nil {
		t.Error("loadConfig(missing, explicit) error = nil, want an error")
	}

	path := filepath.Join(dir, "custom.yaml")
	if err = os.WriteFile(path, []byte("publicnet:\n  interval: 2m\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, source, err = loadConfig(path, true)
	if err != nil || cfg.PublicNet.Interval != 2*time.Minute || source != path {
		t.Errorf("loadConfig(file) = %v, %q, interval %s, want the file's values", err, source, cfg.PublicNet.Interval)
	}
}

func TestNewLogger(t *testing.T) {
	t.Parallel()

	tests := []struct {
		format config.LogFormat
		want   string
	}{
		{config.LogFormatText, `level=WARN msg=shown`},
		{config.LogFormatJSON, `"level":"WARN","msg":"shown"`},
	}

	for _, tt := range tests {
		var out bytes.Buffer

		logger := newLogger(&out, config.Log{Level: slog.LevelWarn, Format: tt.format})
		logger.Info("hidden")
		logger.Warn("shown")

		if got := out.String(); !strings.Contains(got, tt.want) || strings.Contains(got, "hidden") {
			t.Errorf("%s logs = %q, want only the warning", tt.format, got)
		}
	}
}

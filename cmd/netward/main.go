// Command netward monitors the host's public and private network and serves
// the results in a web interface.
//
// Usage:
//
//	netward [-config netward.yaml]
//
// Without -config, netward reads netward.yaml from the working directory if
// it exists and uses the built-in defaults otherwise. See
// netward.example.yaml for every setting.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/devusSs/netward/internal/app"
	"github.com/devusSs/netward/internal/config"
)

const defaultConfigPath = "netward.yaml"

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run starts netward with the command-line arguments args and returns the
// exit code.
func run(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("netward", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", defaultConfigPath, "path of the YAML configuration file")

	err := flags.Parse(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}

		return 2 //nolint:mnd // Exit code for usage errors, like the flag package uses.
	}

	explicit := false

	flags.Visit(func(f *flag.Flag) { explicit = explicit || f.Name == "config" })

	cfg, source, err := loadConfig(*configPath, explicit)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "netward:", err)

		return 1
	}

	logger := newLogger(stderr, cfg.Log)
	logger.Info("configuration loaded", slog.String("source", source))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = app.Run(ctx, cfg, logger)
	if err != nil {
		logger.Error("netward failed", slog.Any("error", err))

		return 1
	}

	return 0
}

// loadConfig loads the configuration at path. A missing file falls back to
// the defaults unless the path was given explicitly. It also returns where
// the configuration came from.
func loadConfig(path string, explicit bool) (config.Config, string, error) {
	cfg, err := config.Load(path)

	switch {
	case err == nil:
		return cfg, path, nil
	case errors.Is(err, fs.ErrNotExist) && !explicit:
		return config.Default(), "defaults (no " + path + ")", nil
	default:
		return config.Config{}, "", err //nolint:wrapcheck // config.Load names the file.
	}
}

func newLogger(w io.Writer, cfg config.Log) *slog.Logger {
	opts := &slog.HandlerOptions{AddSource: false, Level: cfg.Level, ReplaceAttr: nil}

	if cfg.Format == config.LogFormatJSON {
		return slog.New(slog.NewJSONHandler(w, opts))
	}

	return slog.New(slog.NewTextHandler(w, opts))
}

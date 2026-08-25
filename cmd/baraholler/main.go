package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/GeorgeGorbanev/baraholler/internal/config"
	"github.com/GeorgeGorbanev/baraholler/internal/logging"
	"github.com/GeorgeGorbanev/baraholler/internal/observer"
)

var version string

type loggerError struct {
	err    error
	logger *slog.Logger
}

func (e *loggerError) Error() string {
	return e.err.Error()
}

func (e *loggerError) Unwrap() error {
	return e.err
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := execute(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	if err == nil || errors.Is(err, flag.ErrHelp) || errors.Is(err, context.Canceled) {
		return
	}

	logger := logging.New(os.Stderr, slog.LevelInfo, logging.FormatCompact)
	var contextualError *loggerError
	if errors.As(err, &contextualError) {
		logger = contextualError.logger
	}
	logger.Error("baraholler failed", "error", err)
	os.Exit(1)
}

func execute(ctx context.Context, args []string, input io.Reader, output, errorOutput io.Writer) error {
	if len(args) == 0 {
		return runCommand(ctx, nil, input, output, errorOutput)
	}

	switch args[0] {
	case "run":
		return runCommand(ctx, args[1:], input, output, errorOutput)
	case "check-config":
		return checkConfigCommand(args[1:], output, errorOutput)
	case "version":
		return versionCommand(args[1:], output, errorOutput)
	case "help":
		writeUsage(output)
		return nil
	case "-h", "--help":
		writeUsage(output)
		return nil
	default:
		if strings.HasPrefix(args[0], "-") {
			return runCommand(ctx, args, input, output, errorOutput)
		}
		writeUsage(errorOutput)
		return fmt.Errorf("command unknown: %q", args[0])
	}
}

func runCommand(ctx context.Context, args []string, input io.Reader, output, errorOutput io.Writer) error {
	flags := newFlagSet("baraholler run", errorOutput)
	configPath := flags.String("config", "config.yml", "path to the YAML configuration file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("run arguments invalid: unexpected values: %s", strings.Join(flags.Args(), " "))
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	level, err := cfg.LogLevel()
	if err != nil {
		return err
	}
	format, err := cfg.LogFormat()
	if err != nil {
		return err
	}
	logger := logging.New(errorOutput, level, format)
	app, err := observer.New(observer.Options{
		Config:  cfg,
		Logger:  logger,
		Input:   input,
		Output:  output,
		Version: resolvedVersion(),
	})
	if err != nil {
		return &loggerError{err: err, logger: logger}
	}
	if err := app.Run(ctx); err != nil {
		return &loggerError{err: err, logger: logger}
	}
	return nil
}

func checkConfigCommand(args []string, output, errorOutput io.Writer) error {
	flags := newFlagSet("baraholler check-config", errorOutput)
	configPath := flags.String("config", "config.yml", "path to the YAML configuration file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("check-config arguments invalid: unexpected values: %s", strings.Join(flags.Args(), " "))
	}

	if _, err := config.Load(*configPath); err != nil {
		return err
	}
	_, err := fmt.Fprintf(output, "Configuration %q is valid.\n", *configPath)
	return err
}

func versionCommand(args []string, output, errorOutput io.Writer) error {
	flags := newFlagSet("baraholler version", errorOutput)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("version arguments invalid: unexpected values: %s", strings.Join(flags.Args(), " "))
	}
	_, err := fmt.Fprintf(output, "baraholler %s\n", resolvedVersion())
	return err
}

func newFlagSet(name string, errorOutput io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	return flags
}

func resolvedVersion() string {
	if value := strings.TrimSpace(version); value != "" {
		return value
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func writeUsage(output io.Writer) {
	_, _ = fmt.Fprintln(output, `Baraholler watches Telegram marketplace channels for matching terms.

Usage:
  baraholler [run] [-config path]
  baraholler check-config [-config path]
  baraholler version
  baraholler help`)
}

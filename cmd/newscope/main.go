package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/fatih/color"
	"github.com/go-pkgz/lgr"
	"github.com/jessevdk/go-flags"

	"github.com/umputun/newscope/pkg/config"
	"github.com/umputun/newscope/pkg/content"
	"github.com/umputun/newscope/pkg/features"
	"github.com/umputun/newscope/pkg/feed"
	"github.com/umputun/newscope/pkg/llm"
	"github.com/umputun/newscope/pkg/repository"
	"github.com/umputun/newscope/pkg/scheduler"
	"github.com/umputun/newscope/server"
)

// Opts with all CLI options
type Opts struct {
	Config string `short:"c" long:"config" env:"CONFIG" default:"config.yml" description:"configuration file"`

	// common options
	Debug   bool `long:"dbg" env:"DEBUG" description:"debug mode"`
	Version bool `short:"V" long:"version" description:"show version info"`
	NoColor bool `long:"no-color" env:"NO_COLOR" description:"disable color output"`
}

var revision = "unknown"

func main() {
	var opts Opts
	parser := flags.NewParser(&opts, flags.Default)
	if _, err := parser.Parse(); err != nil {
		var flagsErr *flags.Error
		if errors.As(err, &flagsErr) && errors.Is(flagsErr.Type, flags.ErrHelp) {
			os.Exit(0)
		}
		os.Exit(1)
	}

	if opts.Version {
		fmt.Printf("Version: %s\nGolang: %s\n", revision, runtime.Version())
		os.Exit(0)
	}

	// handle termination signals
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	err := run(ctx, opts)
	cancel()

	if err != nil {
		log.Printf("[ERROR] %v", err)
		os.Exit(1)
	}

	log.Print("[INFO] shutdown complete")
}

func run(ctx context.Context, opts Opts) error {
	// load configuration first
	cfg, err := config.Load(opts.Config)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// setup logging with secrets for redaction
	logFile := ""
	if cfg.Log.File.Enabled {
		logFile = cfg.Log.File.Path
	}
	logCloser, err := setupLog(opts.Debug, opts.NoColor, logFile, cfg.LLM.APIKey)
	if err != nil {
		return fmt.Errorf("setup log: %w", err)
	}
	defer logCloser()

	log.Printf("[INFO] starting newscope version %s", revision)

	// setup database repositories
	repoCfg := repository.Config{
		DSN:             cfg.Database.DSN,
		MaxOpenConns:    cfg.Database.MaxOpenConns,
		MaxIdleConns:    cfg.Database.MaxIdleConns,
		ConnMaxLifetime: time.Duration(cfg.Database.ConnMaxLifetime) * time.Second,
	}
	repos, err := repository.NewRepositories(ctx, repoCfg)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer repos.Close()

	// setup LLM classifier - required for system to function
	if cfg.LLM.Endpoint == "" || cfg.LLM.APIKey == "" {
		return fmt.Errorf("LLM classifier is required - missing endpoint or API key configuration")
	}

	// setup feed parser and content extractor
	feedParser := feed.NewParser(cfg.Server.Timeout, cfg.Extraction.UserAgent, cfg.RSSHub.Host)

	var contentExtractor *content.HTTPExtractor
	if cfg.Extraction.Enabled {
		contentExtractor = content.NewHTTPExtractor(cfg.Extraction.Timeout, cfg.Extraction.UserAgent)
		if cfg.Extraction.FallbackURL != "" {
			contentExtractor.SetFallbackURL(cfg.Extraction.FallbackURL)
		}
		contentExtractor.SetOptions(cfg.Extraction.MinTextLength, cfg.Extraction.IncludeImages, cfg.Extraction.IncludeLinks)
		// use retry config from schedule settings
		contentExtractor.SetRetryConfig(
			cfg.Schedule.RetryAttempts,
			cfg.Schedule.RetryInitialDelay,
			cfg.Schedule.RetryMaxDelay,
			cfg.Schedule.RetryJitter,
		)
	}
	classifier := llm.NewClassifier(cfg.LLM)
	log.Printf("[INFO] LLM classifier enabled with model: %s", cfg.LLM.Model)

	// setup and start scheduler
	// warn if jitter is disabled
	if cfg.Schedule.RetryJitter == 0 {
		log.Printf("[WARN] retry jitter is set to 0, this may cause thundering herd problems under high database contention")
	}
	params := scheduler.Params{
		// dependencies
		FeedManager:           repos.Feed,
		ItemManager:           repos.Item,
		ClassificationManager: repos.Classification,
		SettingManager:        repos.Setting,
		Parser:                feedParser,
		Extractor:             contentExtractor,
		Classifier:            classifier,
		// configuration
		UpdateInterval:             cfg.Schedule.UpdateInterval,
		MaxWorkers:                 cfg.Schedule.MaxWorkers,
		PreferenceSummaryThreshold: cfg.LLM.Classification.PreferenceSummaryThreshold,
		CleanupAge:                 cfg.Schedule.CleanupAge,
		CleanupMinScore:            cfg.Schedule.CleanupMinScore,
		CleanupInterval:            cfg.Schedule.CleanupInterval,
		RetryAttempts:              cfg.Schedule.RetryAttempts,
		RetryInitialDelay:          cfg.Schedule.RetryInitialDelay,
		RetryMaxDelay:              cfg.Schedule.RetryMaxDelay,
		RetryJitter:                cfg.Schedule.RetryJitter,
	}

	if features.BeatsEnabled(*cfg) {
		embedder := scheduler.NewOpenAIEmbedder(cfg.Embedding.APIKey, cfg.Embedding.Endpoint, cfg.Embedding.Model)
		params.Embedder = embedder
		params.EmbedStore = repos.Embedding
		params.EmbedModel = cfg.Embedding.Model
	}
	sched := scheduler.NewScheduler(params)
	sched.Start(ctx)
	defer sched.Stop()

	// setup and run server with repository adapter
	repoAdapter := server.NewRepositoryAdapter(repos)
	srv := server.New(cfg, repoAdapter, sched, revision, opts.Debug)
	if err := srv.Run(ctx); err != nil {
		return fmt.Errorf("server failed: %w", err)
	}

	return nil
}

// setupLog configures the logger. When logFile is non-empty, logs are written
// to both stdout and that file; any existing file is rotated to
// <path>.<timestamp> before opening. Returns a closer for the file handle.
func setupLog(dbg, noColor bool, logFile string, secs ...string) (func(), error) {
	logOpts := []lgr.Option{lgr.Msec, lgr.LevelBraces, lgr.StackTraceOnError}
	if dbg {
		logOpts = []lgr.Option{lgr.Debug, lgr.CallerFile, lgr.CallerFunc, lgr.Msec, lgr.LevelBraces, lgr.StackTraceOnError}
	}

	if !noColor {
		colorizer := lgr.Mapper{
			ErrorFunc:  func(s string) string { return color.New(color.FgHiRed).Sprint(s) },
			WarnFunc:   func(s string) string { return color.New(color.FgRed).Sprint(s) },
			InfoFunc:   func(s string) string { return color.New(color.FgYellow).Sprint(s) },
			DebugFunc:  func(s string) string { return color.New(color.FgWhite).Sprint(s) },
			CallerFunc: func(s string) string { return color.New(color.FgBlue).Sprint(s) },
			TimeFunc:   func(s string) string { return color.New(color.FgCyan).Sprint(s) },
		}
		logOpts = append(logOpts, lgr.Map(colorizer))
	}

	if len(secs) > 0 {
		logOpts = append(logOpts, lgr.Secret(secs...))
	}

	closer := func() {}
	if logFile != "" {
		f, err := openLogFileWithRotation(logFile)
		if err != nil {
			return nil, err
		}
		closer = func() { _ = f.Close() }
		// tee stdout and the rotated file; strip ANSI colors from the file copy
		fileNoColor := &ansiStripWriter{w: f}
		logOpts = append(logOpts,
			lgr.Out(io.MultiWriter(os.Stdout, fileNoColor)),
			lgr.Err(io.MultiWriter(os.Stderr, fileNoColor)),
		)
	}

	lgr.SetupStdLogger(logOpts...)
	lgr.Setup(logOpts...)
	return closer, nil
}

// openLogFileWithRotation moves any existing file at path to path.<timestamp>
// and opens a fresh file at path for append-writing.
func openLogFileWithRotation(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		rotated := fmt.Sprintf("%s.%s", path, time.Now().Format("2006-01-02T15-04-05"))
		if err := os.Rename(path, rotated); err != nil {
			return nil, fmt.Errorf("rotate log: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // log file owner-only
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	return f, nil
}

// ansiStripWriter wraps an io.Writer to strip ANSI color escape sequences,
// keeping the on-disk log readable without a terminal.
type ansiStripWriter struct{ w io.Writer }

func (a *ansiStripWriter) Write(p []byte) (int, error) {
	out := make([]byte, 0, len(p))
	for i := 0; i < len(p); i++ {
		if p[i] == 0x1b && i+1 < len(p) && p[i+1] == '[' {
			j := i + 2
			for j < len(p) && (p[j] < 0x40 || p[j] > 0x7e) {
				j++
			}
			if j < len(p) {
				i = j
				continue
			}
		}
		out = append(out, p[i])
	}
	if _, err := a.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

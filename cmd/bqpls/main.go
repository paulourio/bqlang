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
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/paulourio/bqlang/internal/editors"
	"github.com/paulourio/bqlang/internal/lsp"
)

const (
	dirPerm     = 0o750
	logFilePerm = 0o600
)

var version = "dev"

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return runServe(ctx, nil, stdin, stdout, stderr)
	}

	switch args[0] {
	case "-h", "-help", "--help":
		printRootUsage(stdout)

		return 0
	case "serve":
		return runServe(ctx, args[1:], stdin, stdout, stderr)
	case "check", "lint":
		return runCheck(ctx, args[1:], stdin, stdout, stderr)
	case "install":
		return runInstall(ctx, args[1:], stdout, stderr)
	case "export":
		return runExport(args[1:], stdout, stderr)
	case "version":
		return runVersion(args[1:], stdout, stderr)
	default:
		if strings.HasPrefix(args[0], "-") {
			return runServe(ctx, args, stdin, stdout, stderr)
		}

		fmt.Fprintf(stderr, "bqpls: unknown subcommand %q\n", args[0])
		printRootUsage(stderr)

		return 2
	}
}

func printRootUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: bqpls <subcommand> [flags]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Subcommands:")
	fmt.Fprintln(w, "  serve     Start the stdio LSP 3.17 server (default)")
	fmt.Fprintln(w, "  check     Analyze and lint BigQuery SQL files (alias: lint)")
	fmt.Fprintln(w, "  install   Install the embedded editor extension (vscode|kiro|nvim|all)")
	fmt.Fprintln(w, "  export    Export an editor extension bundle (vsix|vscode-dir|nvim)")
	fmt.Fprintln(w, "  version   Print the bqpls version")
}

func runServe(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bqpls serve", flag.ContinueOnError)
	fs.SetOutput(stderr)

	configFlag := fs.String("config", "", "path to .bqfmt.toml configuration file")
	logFileFlag := fs.String("log-file", "", "write server logs to file instead of stderr")
	debugFlag := fs.Bool("debug", false, "enable debug logging")

	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: bqpls serve [-config path] [-log-file path] [-debug]")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}

		return 2
	}

	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "bqpls serve: unexpected positional arguments: %s\n", strings.Join(fs.Args(), " "))
		fs.Usage()

		return 2
	}

	logger, closeLog, err := buildLogger(*logFileFlag, *debugFlag, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "bqpls serve: %v\n", err)

		return 1
	}
	defer func() {
		_ = closeLog()
	}()

	srv, err := lsp.NewServer(lsp.ServerOptions{
		ConfigPath: *configFlag,
		Logger:     logger,
		Version:    versionString(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "bqpls serve: %v\n", err)

		return 1
	}
	defer func() {
		_ = srv.Close()
	}()

	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Serve(sigCtx, stdin, stdout); err != nil {
		if errors.Is(err, context.Canceled) && sigCtx.Err() != nil && ctx.Err() == nil {
			return 0
		}

		fmt.Fprintf(stderr, "bqpls serve: %v\n", err)

		return 1
	}

	return 0
}

func buildLogger(logFilePath string, debug bool, stderr io.Writer) (*slog.Logger, func() error, error) {
	noop := func() error { return nil }
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}

	if strings.TrimSpace(logFilePath) != "" {
		clean := filepath.Clean(logFilePath)
		if err := os.MkdirAll(filepath.Dir(clean), dirPerm); err != nil {
			return nil, noop, fmt.Errorf("create log directory: %w", err)
		}

		f, err := os.OpenFile(clean, os.O_CREATE|os.O_WRONLY|os.O_APPEND, logFilePerm)
		if err != nil {
			return nil, noop, fmt.Errorf("open log file: %w", err)
		}

		return slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: level})), f.Close, nil
	}

	if !debug {
		level = slog.LevelWarn
	}

	return slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level})), noop, nil
}

func runInstall(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	target, binPath, code := parseInstallArgs(args, stderr)
	if code >= 0 {
		return code
	}

	if strings.TrimSpace(binPath) == "" {
		binPath = resolveExecutablePath()
	}

	opts := editors.Options{
		Version: versionString(),
		BinPath: binPath,
	}

	switch target {
	case "vscode":
		if err := editors.InstallVSCode(ctx, opts); err != nil {
			fmt.Fprintf(stderr, "bqpls install vscode: %v\n", err)

			return 1
		}
		fmt.Fprintln(stdout, "installed bqlang extension for VS Code")

		return 0

	case "kiro":
		if err := editors.InstallKiro(ctx, opts); err != nil {
			fmt.Fprintf(stderr, "bqpls install kiro: %v\n", err)

			return 1
		}
		fmt.Fprintln(stdout, "installed bqlang extension for Kiro")

		return 0

	case "nvim":
		if err := editors.InstallNeovim(opts); err != nil {
			fmt.Fprintf(stderr, "bqpls install nvim: %v\n", err)

			return 1
		}
		fmt.Fprintln(stdout, "installed bqpls plugin for Neovim")

		return 0

	case "all":
		return runInstallAll(ctx, opts, stdout, stderr)

	default:
		fmt.Fprintf(stderr, "bqpls install: unknown target %q (expected vscode, kiro, nvim, or all)\n", target)
		printInstallUsage(stderr)

		return 2
	}
}

func parseInstallArgs(args []string, stderr io.Writer) (target, binPath string, exitCode int) {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "bqpls install: missing target (vscode|kiro|nvim|all)")
		printInstallUsage(stderr)

		return "", "", 2
	}

	if args[0] == "-h" || args[0] == "-help" || args[0] == "--help" {
		printInstallUsage(stderr)

		return "", "", 0
	}

	fs := flag.NewFlagSet("bqpls install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	binFlag := fs.String("bin-path", "", "path to bqpls binary stamped into editor configuration")
	fs.Usage = func() {
		printInstallUsage(stderr)
		fs.PrintDefaults()
	}

	flagArgs := args
	positionalFirst := !strings.HasPrefix(args[0], "-")
	if positionalFirst {
		target = args[0]
		flagArgs = args[1:]
	}

	if err := fs.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", "", 0
		}

		return "", "", 2
	}

	if positionalFirst {
		if fs.NArg() > 0 {
			fmt.Fprintf(stderr, "bqpls install: unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
			fs.Usage()

			return "", "", 2
		}

		return target, *binFlag, -1
	}

	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "bqpls install: expected exactly one target (vscode|kiro|nvim|all)")
		fs.Usage()

		return "", "", 2
	}

	return fs.Arg(0), *binFlag, -1
}

func printInstallUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: bqpls install <vscode|kiro|nvim|all> [-bin-path path]")
}

func runInstallAll(ctx context.Context, opts editors.Options, stdout, stderr io.Writer) int {
	failed := false

	if err := editors.InstallVSCode(ctx, opts); err != nil {
		fmt.Fprintf(stderr, "bqpls install all (vscode): %v\n", err)
		failed = true
	} else {
		fmt.Fprintln(stdout, "installed bqlang extension for VS Code")
	}

	if err := editors.InstallKiro(ctx, opts); err != nil {
		fmt.Fprintf(stderr, "bqpls install all (kiro): %v\n", err)
		failed = true
	} else {
		fmt.Fprintln(stdout, "installed bqlang extension for Kiro")
	}

	if err := editors.InstallNeovim(opts); err != nil {
		fmt.Fprintf(stderr, "bqpls install all (nvim): %v\n", err)
		failed = true
	} else {
		fmt.Fprintln(stdout, "installed bqpls plugin for Neovim")
	}

	if failed {
		return 1
	}

	return 0
}

func runExport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bqpls export", flag.ContinueOnError)
	fs.SetOutput(stderr)

	targetFlag := fs.String("target", "", "export target (vsix|vscode-dir|nvim)")
	outputFlag := fs.String("o", "", "output file or directory path")
	binFlag := fs.String("bin-path", "", "optional bqpls binary path stamped into exported assets")

	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: bqpls export -target <vsix|vscode-dir|nvim> -o <path> [-bin-path path]")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}

		return 2
	}

	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "bqpls export: unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		fs.Usage()

		return 2
	}

	target, err := editors.ParseExportTarget(*targetFlag)
	if err != nil {
		fmt.Fprintf(stderr, "bqpls export: %v\n", err)
		fs.Usage()

		return 2
	}

	if strings.TrimSpace(*outputFlag) == "" {
		fmt.Fprintln(stderr, "bqpls export: -o <path> is required")
		fs.Usage()

		return 2
	}

	err = editors.Export(target, *outputFlag, editors.Options{
		Version: versionString(),
		BinPath: *binFlag,
	})
	if err != nil {
		fmt.Fprintf(stderr, "bqpls export: %v\n", err)

		return 1
	}

	fmt.Fprintf(stdout, "exported %s to %s\n", target, *outputFlag)

	return 0
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bqpls version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: bqpls version")
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}

		return 2
	}

	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "bqpls version: unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		fs.Usage()

		return 2
	}

	fmt.Fprintf(stdout, "bqpls %s\n", versionString())

	return 0
}

func resolveExecutablePath() string {
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		return "bqpls"
	}

	return exe
}

func versionString() string {
	if version != "" && version != "dev" {
		return version
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}

	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}

	var (
		rev      string
		modified bool
	)

	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}

	if rev == "" {
		return "dev"
	}

	if len(rev) > 12 {
		rev = rev[:12]
	}

	if modified {
		return "dev+" + rev + "-dirty"
	}

	return "dev+" + rev
}

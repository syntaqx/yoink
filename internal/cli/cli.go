// Package cli is yoink's command line: flags, provider subcommands, and the run that ties
// discovery (providers) to syncing (mirror).
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"

	ucli "github.com/urfave/cli/v3"

	"github.com/syntaqx/yoink/internal/provider"
	"github.com/syntaqx/yoink/internal/provider/azuredevops"
	"github.com/syntaqx/yoink/internal/provider/github"
)

// registry is every provider yoink knows. Adding one is a package plus a line here.
func registry() provider.Registry {
	return provider.Registry{
		github.New(),
		azuredevops.New(),
	}
}

// Version is set at build time with -ldflags "-X github.com/syntaqx/yoink/internal/cli.Version=v1.2.3".
var Version = ""

func version() string {
	if Version != "" {
		return Version
	}
	return moduleVersion(debug.ReadBuildInfo())
}

// moduleVersion is the version go install stamped into the binary, or "dev" for a local build.
func moduleVersion(bi *debug.BuildInfo, ok bool) string {
	if ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

// Run executes yoink with args (excluding the program name) and returns the exit code.
func Run(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runWith(ctx, registry(), args, os.Stdin, os.Stdout, os.Stderr)
}

// runWith is Run with its dependencies passed in, so tests can drive the whole command.
func runWith(ctx context.Context, reg provider.Registry, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := newCommand(reg)
	cmd.Reader, cmd.Writer, cmd.ErrWriter = stdin, stdout, stderr
	err := cmd.Run(ctx, append([]string{"yoink"}, args...))
	if err == nil {
		return 0
	}
	var exit ucli.ExitCoder
	if errors.As(err, &exit) {
		if msg := err.Error(); msg != "" {
			fmt.Fprintln(stderr, "yoink: "+msg)
		}
		return exit.ExitCode()
	}
	fmt.Fprintln(stderr, "yoink: "+err.Error())
	return 2
}

func newCommand(reg provider.Registry) *ucli.Command {
	ucli.VersionFlag = &ucli.BoolFlag{Name: "version", Usage: "print the version"}

	root := &ucli.Command{
		Name:                  "yoink",
		Usage:                 "clone every repository in an organization and keep the clones up to date",
		UsageText:             "yoink <provider> <target>... [flags]\nyoink <url>... [flags]",
		Version:               version(),
		EnableShellCompletion: true,
		HideHelpCommand:       true,
		ExitErrHandler:        func(context.Context, *ucli.Command, error) {}, // Run reports errors
		OnUsageError:          usageError,
		Description: `Clones go under the root directory, one folder per owner, mirroring the host's layout:
<root>/<owner>/<repo>, or <root>/<org>/<project>/<repo> for Azure DevOps. Existing clones
are fetched, switched to their default branch and fast-forwarded; nothing is ever merged,
rebased or discarded without asking.

Ignore patterns are globs matched against the path under the root ("acme/legacy-*",
"*/sandbox", "acme/OldProject"), one per line, from ~/.config/yoink/ignore (every root;
$XDG_CONFIG_HOME is honored), <root>/.yoinkignore (that root), and --ignore. They add up.

Examples:
  yoink github golang
  yoink gh acme acme-labs --root ~/src
  yoink ado contoso/Website -j 16
  yoink https://github.com/golang https://dev.azure.com/contoso`,
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name: "root", Aliases: []string{"r"}, TakesFile: true, Sources: ucli.EnvVars("YOINK_ROOT"),
				Usage: "`DIR` the clones live under (default: ~/Code)",
			},
			&ucli.IntFlag{
				Name: "jobs", Aliases: []string{"j"}, Value: 8, Sources: ucli.EnvVars("YOINK_JOBS"),
				Usage: "sync `N` repositories at once",
				Validator: func(n int) error {
					if n < 1 {
						return fmt.Errorf("--jobs must be at least 1, got %d", n)
					}
					return nil
				},
			},
			&ucli.StringFlag{
				Name: "protocol", Aliases: []string{"p"}, Sources: ucli.EnvVars("YOINK_PROTOCOL"),
				Usage: "clone over `PROTOCOL`, ssh or https (default: per provider)",
				Validator: func(s string) error {
					if p := provider.Protocol(strings.ToLower(s)); p != provider.SSH && p != provider.HTTPS {
						return fmt.Errorf("--protocol must be ssh or https, got %q", s)
					}
					return nil
				},
			},
			&ucli.StringSliceFlag{
				Name: "ignore", Aliases: []string{"i"},
				Usage: "skip repositories matching `GLOB`, e.g. acme/legacy-* (repeatable)",
			},
			&ucli.BoolFlag{Name: "dry-run", Aliases: []string{"n"}, Usage: "show what would be cloned or synced, change nothing"},
			&ucli.BoolFlag{Name: "verbose", Aliases: []string{"v"}, Usage: "show git's output for failures"},
			&ucli.BoolFlag{Name: "no-color", Usage: "disable colors (also NO_COLOR)"},
		},
		// Bare targets are URLs: the host picks the provider.
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			if cmd.NArg() == 0 {
				return ucli.ShowAppHelp(cmd)
			}
			targets, err := targetsFromURLs(reg, cmd.Args().Slice())
			if err != nil {
				return ucli.Exit(err.Error(), 2)
			}
			return execute(ctx, cmd, targets)
		},
	}

	for _, p := range reg {
		info := p.Info()
		root.Commands = append(root.Commands, &ucli.Command{
			Name:         info.Name,
			Aliases:      info.Aliases,
			Usage:        "sync " + info.Title + " repositories",
			ArgsUsage:    info.Usage + "...",
			OnUsageError: usageError,
			Description:  "Auth: " + info.Auth + "\nDefault protocol: " + string(info.Protocol),
			Action: func(ctx context.Context, cmd *ucli.Command) error {
				if cmd.NArg() == 0 {
					return ucli.Exit(fmt.Sprintf("usage: yoink %s %s...", info.Name, info.Usage), 2)
				}
				targets, err := targetsFor(p, cmd.Args().Slice())
				if err != nil {
					return ucli.Exit(err.Error(), 2)
				}
				return execute(ctx, cmd, targets)
			},
		})
	}
	return root
}

// usageError reports a bad flag in one line with a pointer to help, instead of the default: the
// error, the whole help text, then the error again.
func usageError(_ context.Context, cmd *ucli.Command, err error, _ bool) error {
	return ucli.Exit(fmt.Sprintf("%v (see %s --help)", err, cmd.FullName()), 2)
}

// target is one thing to discover: a provider and what to list in it.
type target struct {
	provider provider.Provider
	name     string // e.g. "acme" or "contoso/Website"
}

// targetsFor resolves arguments given to a provider subcommand. Each is a plain name or a URL on
// one of the provider's hosts.
func targetsFor(p provider.Provider, args []string) ([]target, error) {
	var out []target
	for _, a := range args {
		name := a
		if host, path, ok := splitURL(a); ok {
			if (provider.Registry{p}).ForHost(host) == nil {
				return nil, fmt.Errorf("%s is not a %s URL", a, p.Info().Title)
			}
			name = path
		}
		name = cleanTarget(name)
		if name == "" {
			return nil, fmt.Errorf("invalid target %q", a)
		}
		out = append(out, target{p, name})
	}
	return out, nil
}

// targetsFromURLs resolves bare arguments, which must be URLs (scheme optional) on a known host.
func targetsFromURLs(reg provider.Registry, args []string) ([]target, error) {
	var out []target
	for _, a := range args {
		host, path, ok := splitURL(a)
		if !ok {
			return nil, fmt.Errorf("unknown provider %q (try: yoink github %s)", a, a)
		}
		p := reg.ForHost(host)
		if p == nil {
			return nil, fmt.Errorf("no provider for host %q", host)
		}
		name := cleanTarget(path)
		if name == "" {
			return nil, fmt.Errorf("%s names a host but no owner", a)
		}
		out = append(out, target{p, name})
	}
	return out, nil
}

// splitURL splits "https://host/path" or "host.tld/path" into host and path. A bare name with no
// dot before its first slash is not a URL.
func splitURL(s string) (host, path string, ok bool) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		first, _, _ := strings.Cut(s, "/")
		if !strings.Contains(first, ".") {
			return "", "", false
		}
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", "", false
	}
	return u.Hostname(), u.Path, true // Path is already unescaped
}

// cleanTarget trims whitespace and surrounding slashes, so "acme/" and "/acme" mean "acme".
func cleanTarget(s string) string {
	return strings.Trim(strings.TrimSpace(s), "/ \t")
}

// globalIgnoreFile is the per-user ignore file, applied whatever the root:
// $XDG_CONFIG_HOME/yoink/ignore, else ~/.config/yoink/ignore (the same on every OS, as git does).
// It returns "" if no home directory can be found.
func globalIgnoreFile() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "yoink", "ignore")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "yoink", "ignore")
}

// resolveRoot picks the directory clones live under: the flag or YOINK_ROOT if set, else ~/Code.
// A leading ~ is expanded, and the result is absolute.
func resolveRoot(flag string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil && (flag == "" || strings.HasPrefix(flag, "~")) {
		return "", fmt.Errorf("finding home directory: %w", err)
	}
	root := flag
	switch {
	case root == "":
		root = filepath.Join(home, "Code")
	case root == "~":
		root = home
	case strings.HasPrefix(root, "~/"), strings.HasPrefix(root, `~\`):
		root = filepath.Join(home, root[2:])
	}
	return filepath.Abs(root)
}

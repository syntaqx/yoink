package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	ucli "github.com/urfave/cli/v3"

	"github.com/syntaqx/yoink/internal/ignore"
	"github.com/syntaqx/yoink/internal/mirror"
	"github.com/syntaqx/yoink/internal/provider"
	"github.com/syntaqx/yoink/internal/term"
	"github.com/syntaqx/yoink/internal/ui"
)

// session is one invocation: resolved settings plus the output it writes to.
type session struct {
	out      io.Writer
	p        ui.Painter
	live     bool // stdout is a terminal, so the spinner animates
	root     string
	jobs     int
	protocol provider.Protocol // "" means each provider's default
	ignore   *ignore.Matcher
	dryRun   bool
	verbose  bool
}

func execute(ctx context.Context, cmd *ucli.Command, targets []target) error {
	root, err := resolveRoot(cmd.String("root"))
	if err != nil {
		return ucli.Exit(err.Error(), 2)
	}
	ignoreFiles := []string{filepath.Join(root, ".yoinkignore")}
	if global := globalIgnoreFile(); global != "" {
		ignoreFiles = append([]string{global}, ignoreFiles...)
	}
	matcher, err := ignore.Load(ignoreFiles, cmd.StringSlice("ignore"))
	if err != nil {
		return ucli.Exit(err.Error(), 2)
	}
	color := !cmd.Bool("no-color") && os.Getenv("NO_COLOR") == "" &&
		(os.Getenv("FORCE_COLOR") != "" || term.EnableColor(os.Stdout))

	s := &session{
		out:      os.Stdout,
		p:        ui.Painter{Enabled: color},
		live:     term.IsTerminal(os.Stdout),
		root:     root,
		jobs:     cmd.Int("jobs"),
		protocol: provider.Protocol(strings.ToLower(cmd.String("protocol"))),
		ignore:   matcher,
		dryRun:   cmd.Bool("dry-run"),
		verbose:  cmd.Bool("verbose"),
	}
	if !s.run(ctx, targets) {
		return ucli.Exit("", 1)
	}
	return nil
}

// run discovers every target, syncs what it found, and reports. It returns false if anything
// failed.
func (s *session) run(ctx context.Context, targets []target) bool {
	p := s.p
	fmt.Fprintf(s.out, "\n%s\n  %s %s\n\n", p.Banner(), p.Dim("→"), s.root)

	tasks, skipped, ok := s.discover(ctx, targets)
	fmt.Fprintln(s.out)

	if len(tasks) == 0 {
		if ok {
			fmt.Fprintf(s.out, "  %s\n", p.Dim("Nothing to sync."))
		}
		return ok
	}
	if s.dryRun {
		s.plan(tasks)
		return ok
	}

	results := s.sync(ctx, tasks, mirror.Sync)
	failures := s.summarize(results, skipped)
	failures = s.offerReset(ctx, failures)

	if ctx.Err() != nil {
		fmt.Fprintf(s.out, "\n  %s\n", p.Yellow("Interrupted."))
		return false
	}
	if len(failures) == 0 && ok {
		fmt.Fprintf(s.out, "\n  %s\n", p.Green("✓ All repos up to date."))
	}
	return ok && len(failures) == 0
}

// discover lists every target's repositories and turns the ones to sync into tasks. Skipped
// repositories are tallied by reason. ok is false if any target couldn't be listed.
func (s *session) discover(ctx context.Context, targets []target) (tasks []mirror.Task, skipped map[string]int, ok bool) {
	p := s.p
	ok = true
	skipped = map[string]int{}
	seen := map[string]bool{}

	for _, t := range targets {
		info := t.provider.Info()
		label := p.Bold(info.Title) + " " + t.name
		spin := ui.StartSpinner(s.out, p, s.live, func(n int) string {
			return fmt.Sprintf("%s %s", label, p.Dim(fmt.Sprintf("discovering… %d found", n)))
		})
		repos, err := t.provider.Discover(ctx, t.name, spin.Set)
		spin.Stop()
		if err != nil {
			ok = false
			fmt.Fprintf(s.out, "  %s %s\n    %s\n", p.Red("✗"), label, p.Dim(err.Error()))
			continue
		}

		proto := s.protocol
		if proto == "" {
			proto = info.Protocol
		}
		local := map[string]int{}
		queued := 0
		for _, r := range repos {
			reason := r.Skip
			if reason == "" && s.ignore.Match(r.Path) {
				reason = "ignored"
			}
			rel := filepath.FromSlash(r.Path)
			if reason == "" && (!filepath.IsLocal(rel) || !strings.Contains(r.Path, "/")) {
				reason = "unsafe path"
			}
			if reason != "" {
				local[reason]++
				skipped[reason]++
				continue
			}
			key := strings.ToLower(r.Path)
			if seen[key] {
				continue
			}
			seen[key] = true
			queued++
			tasks = append(tasks, mirror.Task{
				Name:     r.Path,
				Dir:      filepath.Join(s.root, rel),
				URL:      r.URL(proto),
				OtherURL: r.OtherURL(proto),
				Branch:   r.DefaultBranch,
			})
		}

		parts := []string{
			plural(len(repos), "repository", "repositories"),
			p.Green(fmt.Sprintf("%d to sync", queued)),
		}
		for _, reason := range sortedKeys(local) {
			parts = append(parts, p.Yellow(fmt.Sprintf("%d %s", local[reason], reason)))
		}
		fmt.Fprintf(s.out, "  %s %s  %s\n", p.Green("✓"), label, strings.Join(parts, p.Dim(" · ")))
	}
	return tasks, skipped, ok
}

// plan prints what a sync would do without doing it.
func (s *session) plan(tasks []mirror.Task) {
	p := s.p
	clones := 0
	for _, t := range tasks {
		if _, err := os.Stat(filepath.Join(t.Dir, ".git")); err == nil {
			fmt.Fprintf(s.out, "  %s %s\n", p.Green("✓"), t.Name)
		} else {
			clones++
			fmt.Fprintf(s.out, "  %s %s %s\n", p.Cyan("+"), t.Name, p.Dim(t.URL))
		}
	}
	fmt.Fprintf(s.out, "\n  %s\n", p.Dim(fmt.Sprintf("Dry run: %d to clone, %d to sync. Nothing was changed.", clones, len(tasks)-clones)))
}

// sync runs work over tasks with live [n/total] progress lines.
func (s *session) sync(ctx context.Context, tasks []mirror.Task, work mirror.Work) []mirror.Result {
	p := s.p
	width := len(strconv.Itoa(len(tasks)))
	results := make([]mirror.Result, 0, len(tasks))
	mirror.Run(ctx, tasks, s.jobs, work, func(r mirror.Result) {
		results = append(results, r)
		counter := p.Grey(fmt.Sprintf("[%*d/%d]", width, len(results), len(tasks)))
		symbol, note := p.Green("✓"), r.Note
		switch r.Status {
		case mirror.Cloned:
			symbol = p.Cyan("+")
			note = strings.TrimSpace("cloned " + paren(r.Note))
		case mirror.Failed:
			symbol, note = p.Red("✗"), string(r.Reason)
			if r.Reason == mirror.Dirty {
				symbol = p.Yellow("✗")
			}
		}
		line := "  " + counter + " " + symbol + " " + r.Task.Name
		if note != "" {
			line += " " + p.Dim(note)
		}
		fmt.Fprintln(s.out, line)
		if s.verbose && r.Log != "" {
			for _, l := range strings.Split(r.Log, "\n") {
				fmt.Fprintln(s.out, "        "+p.Dim(l))
			}
		}
	})
	return results
}

// summarize prints totals and the failures that need attention, and returns the failures.
func (s *session) summarize(results []mirror.Result, skipped map[string]int) []mirror.Result {
	p := s.p
	var current, updated, cloned int
	var dirty, other []mirror.Result
	for _, r := range results {
		switch r.Status {
		case mirror.Current:
			current++
		case mirror.Updated:
			updated++
		case mirror.Cloned:
			cloned++
		case mirror.Failed:
			if r.Reason == mirror.Dirty {
				dirty = append(dirty, r)
			} else {
				other = append(other, r)
			}
		}
	}

	totals := []string{p.Green(fmt.Sprintf("✓ %d synced", current+updated))}
	if updated > 0 {
		totals[0] += p.Dim(fmt.Sprintf(" (%d updated)", updated))
	}
	if cloned > 0 {
		totals = append(totals, p.Cyan(fmt.Sprintf("+ %d cloned", cloned)))
	}
	if n := len(dirty) + len(other); n > 0 {
		totals = append(totals, p.Red(fmt.Sprintf("✗ %d failed", n)))
	}
	for _, reason := range sortedKeys(skipped) {
		totals = append(totals, p.Yellow(fmt.Sprintf("⊘ %d %s", skipped[reason], reason)))
	}
	fmt.Fprintf(s.out, "\n  %s\n  %s\n", p.Dim(strings.Repeat("─", 44)), strings.Join(totals, "   "))

	if len(other) > 0 {
		hint := "fix by hand"
		if !s.verbose {
			hint += "; rerun with -v for git's output"
		}
		fmt.Fprintf(s.out, "\n  %s %s\n", p.Bold(p.Red("Failed")), p.Dim("("+hint+")"))
		for _, r := range other {
			fmt.Fprintf(s.out, "    %s %s %s\n", p.Red("✗"), r.Task.Name, p.Dim(string(r.Reason)))
		}
	}
	if len(dirty) > 0 {
		fmt.Fprintf(s.out, "\n  %s %s\n", p.Bold(p.Yellow("Local changes")), p.Dim("(left untouched)"))
		for _, r := range dirty {
			fmt.Fprintf(s.out, "    %s %s\n", p.Yellow("✗"), r.Task.Name)
		}
	}
	return append(other, dirty...)
}

// offerReset asks whether to discard local changes in the dirty repositories and retry them. It
// only asks on an interactive terminal, and returns the failures that remain.
func (s *session) offerReset(ctx context.Context, failures []mirror.Result) []mirror.Result {
	var dirty []mirror.Task
	var remaining []mirror.Result
	for _, r := range failures {
		if r.Reason == mirror.Dirty {
			dirty = append(dirty, r.Task)
		} else {
			remaining = append(remaining, r)
		}
	}
	if len(dirty) == 0 || ctx.Err() != nil || !term.IsTerminal(os.Stdin) {
		return failures
	}

	p := s.p
	fmt.Fprintf(s.out, "\n  %s ", p.Bold(fmt.Sprintf("Discard local changes in these %d repo(s) with git reset --hard and retry? [y/N]", len(dirty))))
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
		return failures
	}
	fmt.Fprintln(s.out)
	for _, r := range s.sync(ctx, dirty, mirror.Reset) {
		if r.Status == mirror.Failed {
			remaining = append(remaining, r)
		}
	}
	return remaining
}

func paren(s string) string {
	if s == "" {
		return ""
	}
	return "(" + s + ")"
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

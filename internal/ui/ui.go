// Package ui holds yoink's terminal styling: colors, the banner and the discovery spinner.
package ui

import (
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"
)

// Painter wraps text in ANSI styles when Enabled.
type Painter struct{ Enabled bool }

func (p Painter) style(code, s string) string {
	if !p.Enabled || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p Painter) Bold(s string) string   { return p.style("1", s) }
func (p Painter) Dim(s string) string    { return p.style("2", s) }
func (p Painter) Red(s string) string    { return p.style("31", s) }
func (p Painter) Green(s string) string  { return p.style("32", s) }
func (p Painter) Yellow(s string) string { return p.style("33", s) }
func (p Painter) Cyan(s string) string   { return p.style("36", s) }
func (p Painter) Grey(s string) string   { return p.style("90", s) }

// The logo, one figlet glyph per letter; each glyph's rows share a width so they line up.
var glyphs = [][6]string{
	{`       `, ` _   _ `, `| | | |`, `| |_| |`, ` \__, |`, ` |___/ `}, // y
	{`       `, `  ___  `, ` / _ \ `, `| (_) |`, ` \___/ `, `       `}, // o
	{` _ `, `(_)`, `| |`, `| |`, `|_|`, `   `},                         // i
	{`       `, ` _ __  `, `| '_ \ `, `| | | |`, `|_| |_|`, `       `}, // n
	{` _    `, `| | __`, `| |/ /`, `|   < `, `|_|\_\`, `      `},       // k
}

// Banner returns the yoink logo, indented and colored.
func (p Painter) Banner() string {
	var b strings.Builder
	for row := range 6 {
		var line strings.Builder
		for _, g := range glyphs {
			line.WriteString(g[row])
		}
		if l := strings.TrimRight(line.String(), " "); l != "" {
			b.WriteString("  " + p.Cyan(l) + "\n")
		}
	}
	return b.String()
}

var frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner redraws one status line until stopped. Set may be called from any goroutine.
type Spinner struct {
	w     io.Writer
	p     Painter
	label func(n int) string
	n     atomic.Int64
	stop  chan struct{}
	done  chan struct{}
}

// StartSpinner starts animating label(n) on w. When live is false (output isn't a terminal) it
// draws nothing.
func StartSpinner(w io.Writer, p Painter, live bool, label func(n int) string) *Spinner {
	s := &Spinner{w: w, p: p, label: label, stop: make(chan struct{}), done: make(chan struct{})}
	if !live {
		close(s.done)
		return s
	}
	go s.loop()
	return s
}

// Set updates the count shown by the label.
func (s *Spinner) Set(n int) { s.n.Store(int64(n)) }

// Stop erases the spinner line. It is safe to call more than once.
func (s *Spinner) Stop() {
	select {
	case <-s.done:
		return
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
}

func (s *Spinner) loop() {
	defer close(s.done)
	tick := time.NewTicker(80 * time.Millisecond)
	defer tick.Stop()
	for i := 0; ; i++ {
		fmt.Fprintf(s.w, "\r\x1b[K  %s %s", s.p.Cyan(frames[i%len(frames)]), s.label(int(s.n.Load())))
		select {
		case <-s.stop:
			fmt.Fprint(s.w, "\r\x1b[K")
			return
		case <-tick.C:
		}
	}
}

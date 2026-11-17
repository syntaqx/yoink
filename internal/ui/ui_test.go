package ui

import (
	"bytes"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestPainter(t *testing.T) {
	on, off := Painter{Enabled: true}, Painter{}
	for _, c := range []struct {
		name  string
		style func(Painter, string) string
		code  string
	}{
		{"Bold", Painter.Bold, "1"},
		{"Dim", Painter.Dim, "2"},
		{"Red", Painter.Red, "31"},
		{"Green", Painter.Green, "32"},
		{"Yellow", Painter.Yellow, "33"},
		{"Cyan", Painter.Cyan, "36"},
		{"Grey", Painter.Grey, "90"},
	} {
		if got, want := c.style(on, "hi"), "\x1b["+c.code+"mhi\x1b[0m"; got != want {
			t.Errorf("%s enabled = %q, want %q", c.name, got, want)
		}
		if got := c.style(off, "hi"); got != "hi" {
			t.Errorf("%s disabled = %q, want plain", c.name, got)
		}
		if got := c.style(on, ""); got != "" {
			t.Errorf("%s of empty = %q, want empty (no stray escapes)", c.name, got)
		}
	}
}

func TestGlyphRowsShareAWidth(t *testing.T) {
	// The logo lines up only if every row of a glyph is as wide as its others.
	for i, g := range glyphs {
		w := utf8.RuneCountInString(g[0])
		for row, s := range g {
			if n := utf8.RuneCountInString(s); n != w {
				t.Errorf("glyph %d row %d is %d wide, row 0 is %d: %q", i, row, n, w, s)
			}
		}
	}
}

func TestBanner(t *testing.T) {
	plain := Painter{}.Banner()
	want := "" +
		"                 _         _\n" +
		"   _   _   ___  (_) _ __  | | __\n" +
		"  | | | | / _ \\ | || '_ \\ | |/ /\n" +
		"  | |_| || (_) || || | | ||   <\n" +
		"   \\__, | \\___/ |_||_| |_||_|\\_\\\n" +
		"   |___/\n"
	if plain != want {
		t.Fatalf("banner:\n%s\nwant:\n%s", plain, want)
	}

	// Colored, each line is the same text wrapped in cyan after the indent.
	lines := strings.Split(strings.TrimSuffix(plain, "\n"), "\n")
	on := Painter{Enabled: true}
	for i, l := range strings.Split(strings.TrimSuffix(on.Banner(), "\n"), "\n") {
		if want := "  " + on.Cyan(strings.TrimPrefix(lines[i], "  ")); l != want {
			t.Errorf("colored line %d = %q, want %q", i, l, want)
		}
	}
}

// syncBuffer is a bytes.Buffer the spinner goroutine and the test can share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSpinnerNotLiveDrawsNothing(t *testing.T) {
	var buf syncBuffer
	s := StartSpinner(&buf, Painter{}, false, func(int) string { return "label" })
	s.Set(3)
	s.Stop()
	s.Stop()
	if got := buf.String(); got != "" {
		t.Errorf("wrote %q, want nothing", got)
	}
}

func TestSpinnerLive(t *testing.T) {
	var buf syncBuffer
	s := StartSpinner(&buf, Painter{}, true, func(n int) string { return "found " + strconv.Itoa(n) })
	waitFor(t, "the first frame", func() bool { return strings.Contains(buf.String(), "found 0") })
	s.Set(42)
	waitFor(t, "the updated count", func() bool { return strings.Contains(buf.String(), "found 42") })
	s.Stop()
	s.Stop() // idempotent

	out := buf.String()
	if !strings.HasPrefix(out, "\r\x1b[K  ⠋ found 0") {
		t.Errorf("first frame = %q", out[:min(len(out), 40)])
	}
	if !strings.HasSuffix(out, "found 42\r\x1b[K") {
		t.Errorf("the line isn't erased on Stop: %q", out[max(0, len(out)-40):])
	}
	frames := strings.Count(out, "\r\x1b[K  ")
	if frames < 2 {
		t.Errorf("drew %d frames, want the spinner to animate", frames)
	}
	if got := buf.String(); got != out {
		t.Error("spinner wrote after Stop returned")
	}
}

func TestSpinnerSetFromManyGoroutines(t *testing.T) {
	var buf syncBuffer
	s := StartSpinner(&buf, Painter{Enabled: true}, true, strconv.Itoa)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				s.Set(g*1000 + i)
			}
		}()
	}
	wg.Wait()
	s.Set(-1) // a count none of the goroutines set, so only the final Set can draw it
	waitFor(t, "the final count", func() bool { return strings.HasSuffix(buf.String(), " -1") })
	s.Stop()
}

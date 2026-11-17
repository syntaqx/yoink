// Package ignore decides which repositories to leave alone, from glob patterns such as
// "acme/legacy-*" or "*/sandbox", matched against a repository's path under the root.
package ignore

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
)

// Matcher holds ignore patterns. The zero value ignores nothing.
type Matcher struct {
	patterns []string
}

// Load reads patterns from file (a missing file is fine) and appends extra. Blank lines and lines
// starting with # are skipped.
func Load(file string, extra []string) (*Matcher, error) {
	m := &Matcher{}
	f, err := os.Open(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if line := strings.TrimSpace(sc.Text()); line != "" && !strings.HasPrefix(line, "#") {
				m.patterns = append(m.patterns, line)
			}
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	m.patterns = append(m.patterns, extra...)
	for i, p := range m.patterns {
		p = strings.ToLower(strings.Trim(strings.ReplaceAll(p, `\`, "/"), "/"))
		if _, err := path.Match(p, ""); err != nil {
			return nil, fmt.Errorf("bad ignore pattern %q: %w", m.patterns[i], err)
		}
		m.patterns[i] = p
	}
	return m, nil
}

// Match reports whether the slash-separated repository path, or any directory above it, matches a
// pattern, ignoring case. So "acme/legacy" ignores a repository by that name, and, for nested
// layouts, everything inside a directory by that name.
func (m *Matcher) Match(repoPath string) bool {
	segments := strings.Split(strings.ToLower(repoPath), "/")
	for i := range segments {
		prefix := strings.Join(segments[:i+1], "/")
		for _, p := range m.patterns {
			if ok, _ := path.Match(p, prefix); ok {
				return true
			}
		}
	}
	return false
}

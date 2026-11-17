//go:build !windows

package term

import "os"

// enableVT is a no-op: Unix terminals handle ANSI escapes natively.
func enableVT(*os.File) bool { return true }

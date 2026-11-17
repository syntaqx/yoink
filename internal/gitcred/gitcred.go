// Package gitcred reads credentials from git's configured credential helper (for example Git
// Credential Manager), so providers can reuse a sign-in the user already has.
package gitcred

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strings"
)

// Fill asks git's credential helper for the password (or token) it stores for
// https://host/path. It never prompts; it returns "" when no credential is stored.
func Fill(ctx context.Context, host, path string) string {
	input := "protocol=https\nhost=" + host + "\n"
	if path != "" {
		input += "path=" + path + "\n"
	}
	cmd := exec.CommandContext(ctx, "git", "credential", "fill")
	cmd.Stdin = strings.NewReader(input + "\n")
	// GIT_TERMINAL_PROMPT=0 alone still lets git run an askpass program (GIT_ASKPASS, core.askPass,
	// SSH_ASKPASS; VS Code's terminal sets GIT_ASKPASS). An empty GIT_ASKPASS overrides all three.
	cmd.Env = append(os.Environ(), "GCM_INTERACTIVE=never", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimRight(sc.Text(), "\r"), "password="); ok {
			return v
		}
	}
	return ""
}

package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The tests run this test binary again as the real program: with YOINK_TEST_MAIN set, TestMain
// hands the arguments to main instead of running tests. That checks what only a real process
// shows, the exit code os.Exit reports.
func TestMain(m *testing.M) {
	if os.Getenv("YOINK_TEST_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

func yoink(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "YOINK_TEST_MAIN=1", "NO_COLOR=1")
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	return cmd.ProcessState.ExitCode(), out.String(), errOut.String()
}

func TestExitCodes(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{[]string{"--version"}, 0, "yoink version ", ""},
		{[]string{"--help"}, 0, "yoink <provider> <target>...", ""},
		{[]string{"github", "acme", "--jobs", "0"}, 2, "", "yoink: invalid value \"0\" for flag -jobs"},
		{[]string{"not-a-provider"}, 2, "", `yoink: unknown provider "not-a-provider"`},
	} {
		code, stdout, stderr := yoink(t, tc.args...)
		if code != tc.code || !strings.Contains(stdout, tc.stdout) || !strings.Contains(stderr, tc.stderr) {
			t.Errorf("yoink %s: exit %d, stdout %q, stderr %q", strings.Join(tc.args, " "), code, stdout, stderr)
		}
	}
}

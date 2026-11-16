package main

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCLIArguments(t *testing.T) {
	if args := flag.Args(); len(args) > 0 && args[0] == "proxy-test-main" {
		os.Args = append([]string{"codex-oauth-proxy"}, args[1:]...)
		flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
		main()
		return
	}

	for _, test := range []struct {
		name     string
		args     []string
		wantExit int
		wantText string
	}{
		{"Docker command", []string{"./codex-oauth-proxy", "--creds-store=env"}, 2, "unexpected positional arguments"},
		{"Kubernetes args", []string{"/app/codex-oauth-proxy", "--creds-store=xdg", "--creds-path=/config/auth.json"}, 2, "unexpected positional arguments"},
		{"positional text", []string{"unexpected-input"}, 2, "unexpected positional arguments"},
		{"flags with help", []string{"--creds-store=xdg", "--creds-path=/config/auth.json", "--help"}, 0, "Usage of codex-oauth-proxy:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := test.args
			if test.wantExit != 0 {
				// An invalid store keeps a missing argument guard from touching credentials.
				args = append([]string{"--creds-store=invalid"}, args...)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], append([]string{
				"-test.run=^TestCLIArguments$", "--", "proxy-test-main",
			}, args...)...)
			output, err := cmd.CombinedOutput()
			if cmd.ProcessState == nil {
				t.Fatalf("start CLI: %v", err)
			}
			if got := cmd.ProcessState.ExitCode(); got != test.wantExit {
				t.Fatalf("exit code = %d, want %d; output:\n%s", got, test.wantExit, output)
			}
			if !strings.Contains(string(output), test.wantText) {
				t.Errorf("output = %q, want %q", output, test.wantText)
			}
		})
	}
}

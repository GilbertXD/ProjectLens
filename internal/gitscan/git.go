package gitscan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type gitRunner struct {
	token    string
	testHTTP bool
}

func (g gitRunner) command(ctx context.Context, auth bool, args ...string) *exec.Cmd {
	protocol := "https"
	if g.testHTTP {
		protocol = "http"
	}
	flags := []string{
		"--no-replace-objects",
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.symlinks=false",
		"-c", "core.fsmonitor=false",
		"-c", "submodule.recurse=false",
		"-c", "credential.helper=",
		"-c", "protocol.allow=never",
		"-c", "protocol.file.allow=never",
		"-c", "protocol." + protocol + ".allow=always",
		"-c", "http.followRedirects=false",
		"-c", "fetch.writeCommitGraph=false",
		"-c", "gc.auto=0",
	}
	cmd := exec.CommandContext(ctx, "git", append(flags, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") && !strings.HasPrefix(entry, "LC_ALL=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	allowedProtocol := ""
	if auth || len(args) > 0 && args[0] == "ls-remote" {
		allowedProtocol = protocol
	}
	cmd.Env = append(cmd.Env, "LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_OPTIONAL_LOCKS=0", "GIT_SSH_COMMAND=false", "GIT_ASKPASS=",
		"GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL="+allowedProtocol)
	if auth && g.token != "" && !g.testHTTP {
		cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Bearer "+g.token)
	}
	return cmd
}

type boundedOutput struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := b.limit - b.buffer.Len(); n > remaining {
		b.truncated = true
		p = p[:remaining]
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}

type gitError struct {
	err        error
	diagnostic string
}

func (e *gitError) Error() string { return fmt.Sprintf("git command failed: %v", e.err) }
func (e *gitError) Unwrap() error { return e.err }

func (g gitRunner) run(ctx context.Context, limit int, auth bool, args ...string) ([]byte, error) {
	cmd := g.command(ctx, auth, args...)
	stdout := &boundedOutput{limit: limit}
	stderr := &boundedOutput{limit: 8192}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		// Stderr may echo request headers or repository-controlled values. Keep
		// it private and use it only for classifying Git failures.
		diagnostic := stderr.buffer.String()
		if g.token != "" {
			diagnostic = strings.ReplaceAll(diagnostic, g.token, "[redacted]")
		}
		return nil, &gitError{err: err, diagnostic: diagnostic}
	}
	if stdout.truncated {
		return nil, errors.New("Git output exceeded the scanner limit")
	}
	return stdout.buffer.Bytes(), nil
}

func (g gitRunner) resolve(ctx context.Context, dir, ref string) (string, error) {
	out, err := g.run(ctx, 1024, false, "--git-dir="+dir, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	oid := strings.TrimSpace(string(out))
	if !validOID(oid) {
		return "", errors.New("Git returned an invalid commit ID")
	}
	return oid, nil
}

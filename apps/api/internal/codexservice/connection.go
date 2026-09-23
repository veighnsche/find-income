package codexservice

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

var ErrUnavailable = errors.New("Codex runtime unavailable")
var ErrBusy = errors.New("Codex intake is active")

type Config struct {
	Host, User, IdentityFile, KnownHostsFile, Launcher string
	IsolationVerified                                  bool
	BridgeToken                                        string
	BridgeName                                         string
}

func configFromEnvironment() Config {
	return Config{Host: os.Getenv("JOBSEEK_CODEX_SSH_HOST"), User: os.Getenv("JOBSEEK_CODEX_SSH_USER"),
		IdentityFile: os.Getenv("JOBSEEK_CODEX_SSH_IDENTITY_FILE"), KnownHostsFile: os.Getenv("JOBSEEK_CODEX_SSH_KNOWN_HOSTS"),
		Launcher: os.Getenv("JOBSEEK_CODEX_REMOTE_LAUNCHER"), IsolationVerified: os.Getenv("JOBSEEK_CODEX_ISOLATION_VERIFIED") == "true",
		BridgeToken: os.Getenv("JOBSEEK_CODEX_BRIDGE_TOKEN"), BridgeName: "jobseek"}
}

var hostPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`)
var userPattern = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_-]*$`)
var launcherPattern = regexp.MustCompile(`^/[a-zA-Z0-9_./-]+$`)

func (c Config) unavailableCode() string {
	if c.Host == "" || c.User == "" || c.Launcher == "" || c.IdentityFile == "" || c.KnownHostsFile == "" {
		return "runner_not_configured"
	}
	if !c.IsolationVerified {
		return "isolation_not_verified"
	}
	if !hostPattern.MatchString(c.Host) || !userPattern.MatchString(c.User) || !launcherPattern.MatchString(c.Launcher) ||
		!filepath.IsAbs(c.IdentityFile) || !filepath.IsAbs(c.KnownHostsFile) || strings.HasPrefix(c.Host, "-") {
		return "runner_configuration_invalid"
	}
	if len(c.BridgeToken) < 32 || strings.TrimSpace(c.BridgeToken) != c.BridgeToken {
		return "bridge_not_configured"
	}
	return ""
}

// SSH authenticates the configured control channel. IsolationVerified is an
// operator assertion after actual host verification, not a fact proved by SSH.
// There is deliberately no local Codex execution fallback.
func dialSSH(ctx context.Context, cfg Config) (io.ReadWriteCloser, error) {
	if cfg.unavailableCode() != "" {
		return nil, ErrUnavailable
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/ssh", "-F", "/dev/null", "-T",
		"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "IdentitiesOnly=yes",
		"-o", "ClearAllForwardings=yes", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2",
		"-o", "UserKnownHostsFile="+cfg.KnownHostsFile, "-i", cfg.IdentityFile,
		"--", cfg.User+"@"+cfg.Host, cfg.Launcher)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	cmd.Stderr = io.Discard
	return startSSHTransport(cmd)
}

// Owned pipe descriptors stay readable after cmd.Wait reaps a fast-exiting
// launcher. exec.Cmd's StdoutPipe would be closed by Wait before final drain.
func startSSHTransport(cmd *exec.Cmd) (io.ReadWriteCloser, error) {
	inRead, in, err := os.Pipe()
	if err != nil {
		return nil, ErrUnavailable
	}
	defer inRead.Close()
	out, outWrite, err := os.Pipe()
	if err != nil {
		in.Close()
		return nil, ErrUnavailable
	}
	defer outWrite.Close()
	cmd.Stdin, cmd.Stdout = inRead, outWrite
	if cmd.Start() != nil {
		in.Close()
		out.Close()
		return nil, ErrUnavailable
	}
	t := &sshTransport{in: in, out: out, cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(t.done) }()
	return t, nil
}

type sshTransport struct {
	in   io.WriteCloser
	out  io.ReadCloser
	cmd  *exec.Cmd
	done chan struct{}
	once sync.Once
}

func (t *sshTransport) Read(p []byte) (int, error)  { return t.out.Read(p) }
func (t *sshTransport) Write(p []byte) (int, error) { return t.in.Write(p) }
func (t *sshTransport) Close() error {
	t.once.Do(func() { _ = t.in.Close(); _ = t.out.Close(); _ = t.cmd.Process.Kill(); <-t.done })
	return nil
}

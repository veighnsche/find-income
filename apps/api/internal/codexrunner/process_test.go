//go:build (darwin || linux) && (amd64 || arm64)

package codexrunner

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codex"
)

// Helpers are this native test executable, never Codex or a model/tool runner.
// Mode is a synthetic work-directory fixture; production has no args/env hook.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "descendant" {
		fmt.Println(os.Getpid())
		for {
			time.Sleep(time.Hour)
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "app-server" {
		mode, _ := os.ReadFile("mode")
		switch string(mode) {
		case "echo":
			fmt.Println("ready")
			io.Copy(os.Stdout, os.Stdin)
		case "env":
			cwd, _ := os.Getwd()
			json.NewEncoder(os.Stdout).Encode(struct {
				Env, Args []string
				Cwd       string
			}{os.Environ(), os.Args[1:], cwd})
			io.Copy(io.Discard, os.Stdin)
		case "stderr":
			for i := 0; i < 4096; i++ {
				fmt.Fprintln(os.Stderr, strings.Repeat("synthetic-stderr-secret", 128))
			}
			fmt.Println("ready")
			io.Copy(io.Discard, os.Stdin)
		case "eof":
			os.Stdout.Close()
			for {
				time.Sleep(time.Hour)
			}
		case "crash":
			os.Exit(17)
		case "group", "group-crash":
			child := exec.Command(os.Args[0], "descendant")
			child.Env = os.Environ()
			child.Stdout = os.Stdout
			child.Stderr = os.Stderr
			if child.Start() != nil {
				os.Exit(19)
			}
			if string(mode) == "group-crash" {
				var b [1]byte
				os.Stdin.Read(b[:])
				os.Exit(17)
			}
			for {
				time.Sleep(time.Hour)
			}
		case "flood":
			fmt.Println("ready")
			block := make([]byte, 1<<20)
			for {
				if _, err := os.Stdout.Write(block); err != nil {
					os.Exit(0)
				}
			}
		case "idle":
			fmt.Println("ready")
			for {
				time.Sleep(time.Hour)
			}
		default:
			os.Exit(18)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fixture(t *testing.T, mode string) Config {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, work := filepath.Join(base, "state"), filepath.Join(base, "work")
	for _, dir := range []string{state, work} {
		if err = os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(work, "mode"), []byte(mode), 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(contents)
	return Config{Executable: binary, SHA256: hex.EncodeToString(hash[:]), StateDir: state, WorkDir: work}
}

func launch(t *testing.T, ctx context.Context, cfg Config) *Process {
	t.Helper()
	p, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil && !(runtime.GOOS == "darwin" && err == ErrCleanup && p.impl.cmd.ProcessState != nil && p.impl.cmd.ProcessState.Exited()) {
			t.Error(err)
		}
	})
	return p
}

func waitDone(t *testing.T, p *Process) {
	t.Helper()
	select {
	case <-p.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("lifecycle did not join")
	}
}

func line(t *testing.T, p *Process) string {
	t.Helper()
	result := make(chan string, 1)
	go func() { s, _ := bufio.NewReader(p).ReadString('\n'); result <- strings.TrimSpace(s) }()
	select {
	case s := <-result:
		return s
	case <-time.After(3 * time.Second):
		p.Close()
		t.Fatal("missing helper readiness")
		return ""
	}
}

func TestPinnedExecutableAndConfiguration(t *testing.T) {
	cfg := fixture(t, "idle")
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
		want   error
	}{
		{"wrong hash", func(c *Config) { c.SHA256 = strings.Repeat("00", 32) }, ErrExecutable},
		{"missing hash", func(c *Config) { c.SHA256 = "" }, ErrExecutable},
		{"missing binary", func(c *Config) { c.Executable += "-synthetic-secret-missing" }, ErrExecutable},
		{"relative binary", func(c *Config) { c.Executable = "binary" }, ErrExecutable},
		{"overlapping dirs", func(c *Config) { c.WorkDir = c.StateDir }, ErrConfiguration},
		{"missing state", func(c *Config) { c.StateDir += "/missing" }, ErrConfiguration},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := cfg
			tc.mutate(&c)
			p, err := Start(context.Background(), c)
			if p != nil || err != tc.want {
				t.Fatalf("p=%v err=%v", p, err)
			}
		})
	}
	if _, err := Start(nil, cfg); err != ErrConfiguration {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Start(ctx, cfg); err != ErrCanceled {
		t.Fatal(err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := Start(ctx, cfg); err != ErrDeadline {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg.StateDir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), cfg); err != ErrConfiguration {
		t.Fatal(err)
	}
}

func TestStartupFailureRedacted(t *testing.T) {
	cfg := fixture(t, "idle")
	cfg.Executable = filepath.Join(filepath.Dir(cfg.StateDir), "synthetic-secret-not-executable-format")
	data := []byte("synthetic invalid native executable; secret payload")
	if err := os.WriteFile(cfg.Executable, data, 0500); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	cfg.SHA256 = hex.EncodeToString(hash[:])
	if p, err := Start(context.Background(), cfg); p != nil || err != ErrStart {
		t.Fatalf("unexpected start result %v %v", p, err)
	}
}

func TestMinimalEnvironmentAndArguments(t *testing.T) {
	// These are invented test names/values, never reads or copies of user secrets.
	t.Setenv("CODEXRUNNER_SYNTHETIC_ADMIN_SECRET", "sentinel")
	t.Setenv("OPENAI_API_KEY", "synthetic-do-not-forward")
	t.Setenv("CODEX_HOME", "/synthetic-wrong-state")
	cfg := fixture(t, "env")
	p := launch(t, context.Background(), cfg)
	var got struct {
		Env, Args []string
		Cwd       string
	}
	if err := json.Unmarshal([]byte(line(t, p)), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"HOME": cfg.StateDir, "CODEX_HOME": cfg.StateDir, "TMPDIR": cfg.StateDir, "PATH": "/usr/bin:/bin", "LANG": "C", "LC_ALL": "C", "TZ": "UTC"}
	if len(got.Env) != len(want) {
		t.Fatalf("environment count %d", len(got.Env))
	}
	for _, entry := range got.Env {
		k, v, ok := strings.Cut(entry, "=")
		if !ok || want[k] != v {
			t.Fatal("unexpected child environment entry")
		}
		delete(want, k)
	}
	if len(want) != 0 || got.Cwd != cfg.WorkDir || strings.Join(got.Args, " ") != "app-server --strict-config --listen stdio://" {
		t.Fatal("wrong launch context")
	}
}

func TestStderrDiscardAndTransportCompatibility(t *testing.T) {
	p := launch(t, context.Background(), fixture(t, "stderr"))
	if line(t, p) != "ready" {
		t.Fatal("stderr blocked helper")
	}
	var transport codex.Transport = p
	client, err := codex.NewClient(transport, codex.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	waitDone(t, p)
}

func TestEOFAndCrash(t *testing.T) {
	for _, mode := range []string{"eof", "crash"} {
		t.Run(mode, func(t *testing.T) {
			p := launch(t, context.Background(), fixture(t, mode))
			if mode == "eof" {
				var b [1]byte
				if _, err := p.Read(b[:]); err != io.EOF {
					t.Fatal(err)
				}
			}
			waitDone(t, p)
			if p.Err() != ErrExited && p.Err() != ErrTransport && !(runtime.GOOS == "darwin" && mode == "crash" && p.Err() == ErrCleanup) {
				t.Fatal(p.Err())
			}
			if p.impl.cmd.ProcessState == nil {
				t.Fatal("leader not reaped")
			}
		})
	}
}

func TestBlockedPipesAndConcurrentClose(t *testing.T) {
	p := launch(t, context.Background(), fixture(t, "idle"))
	if line(t, p) != "ready" {
		t.Fatal("readiness")
	}
	readDone, writeDone := make(chan error, 1), make(chan error, 1)
	go func() { var b [1]byte; _, err := p.Read(b[:]); readDone <- err }()
	go func() { _, err := p.Write(make([]byte, 16<<20)); writeDone <- err }()
	// Helper announced readiness and never reads stdin or writes again. Give
	// both operations time to enter their pipe calls, requiring neither to
	// complete until Close unblocks them.
	select {
	case <-readDone:
		t.Fatal("read did not block")
	case <-writeDone:
		t.Fatal("write did not block")
	case <-time.After(30 * time.Millisecond):
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	waitDone(t, p)
	for _, ch := range []chan error{readDone, writeDone} {
		select {
		case err := <-ch:
			if err == nil {
				t.Fatal("expected interrupted IO")
			}
		case <-time.After(time.Second):
			t.Fatal("blocked IO retained")
		}
	}
	if p.Err() != ErrClosed && p.Err() != ErrTransport {
		t.Fatal(p.Err())
	}
}

func TestContextCancellationAndDeadline(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint(deadline), func(t *testing.T) {
			cfg := fixture(t, "flood")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 250*time.Millisecond)
				defer cancel()
			}
			p := launch(t, ctx, cfg)
			if line(t, p) != "ready" {
				t.Fatal("readiness")
			}
			if !deadline {
				cancel()
			}
			waitDone(t, p)
			want := ErrCanceled
			if deadline {
				want = ErrDeadline
			}
			if p.Err() != want {
				t.Fatal(p.Err())
			}
		})
	}
}

func TestOwnedDescendantCleanup(t *testing.T) {
	callerGroup := syscall.Getpgrp()
	for _, mode := range []string{"group", "group-crash"} {
		t.Run(mode, func(t *testing.T) {
			p := launch(t, context.Background(), fixture(t, mode))
			child, err := strconv.Atoi(line(t, p))
			if err != nil {
				t.Fatal("child readiness")
			}
			pgid, err := syscall.Getpgid(child)
			if err != nil || pgid != p.impl.cmd.Process.Pid || pgid == callerGroup {
				t.Fatal("incorrect group ownership")
			}
			if mode == "group-crash" {
				if _, err = p.Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
				waitDone(t, p)
			} else {
				if err = p.Close(); err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(2 * time.Second)
			for syscall.Kill(child, 0) == nil && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if err = syscall.Kill(child, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatal("descendant still present (including possible unreaped zombie)")
			}
			if syscall.Getpgrp() != callerGroup || syscall.Kill(-callerGroup, 0) != nil {
				t.Fatal("caller group changed")
			}
		})
	}
}

func TestWaitableKeepsLeaderReserved(t *testing.T) {
	cfg := fixture(t, "crash")
	cmd := exec.Command(cfg.Executable, "app-server")
	cmd.Dir = cfg.WorkDir
	cmd.Env = []string{"LANG=C"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	defer cmd.Process.Kill()
	deadline := time.Now().Add(time.Second)
	for {
		exited, err := waitable(cmd.Process.Pid)
		if err != nil {
			t.Fatalf("waitid errno: %v", err)
		}
		if exited {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("exit detection timeout")
		}
		time.Sleep(time.Millisecond)
	}
	if exited, err := waitable(cmd.Process.Pid); err != nil || !exited {
		t.Fatalf("second waitid: exited=%v errno=%v", exited, err)
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err != nil && err != syscall.ESRCH && !(runtime.GOOS == "darwin" && err == syscall.EPERM) {
		t.Fatalf("group kill errno=%v", err)
	}
	if runtime.GOOS == "darwin" && err == syscall.EPERM {
		t.Log("Darwin zombie-only group is conservatively reported as cleanup failure; WNOWAIT still retained leader")
	}
}

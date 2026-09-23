//go:build (darwin || linux) && (amd64 || arm64)

package codexrunner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

type process struct {
	stdin, stdout *os.File
	cmd           *exec.Cmd
	stop          chan error
	done          chan struct{}
	closePipes    sync.Once
	terminal      error // written before done is closed, read only after done
	cleanup       error
}

func start(ctx context.Context, cfg Config) (*Process, error) {
	if ctx == nil {
		return nil, ErrConfiguration
	}
	if ctx.Err() != nil {
		return nil, contextError(ctx)
	}
	if !privateDir(cfg.StateDir) || !privateDir(cfg.WorkDir) || within(cfg.StateDir, cfg.WorkDir) || within(cfg.WorkDir, cfg.StateDir) {
		return nil, ErrConfiguration
	}
	if within(cfg.StateDir, cfg.Executable) || within(cfg.WorkDir, cfg.Executable) {
		return nil, ErrConfiguration
	}
	if err := verifyExecutable(ctx, cfg); err != nil {
		return nil, err
	}
	// No os.Environ, command shell, arbitrary arguments or env extension hook.
	cmd := exec.Command(cfg.Executable, "app-server", "--strict-config", "--listen", "stdio://")
	cmd.Dir = cfg.WorkDir
	cmd.Env = []string{"HOME=" + cfg.StateDir, "CODEX_HOME=" + cfg.StateDir, "TMPDIR=" + cfg.StateDir,
		"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "TZ=UTC"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Own both pipe ends explicitly. exec.Cmd has no copying goroutines that
	// could wait forever on a descendant keeping a descriptor open.
	inRead, inWrite, err := os.Pipe()
	if err != nil {
		return nil, ErrStart
	}
	defer inRead.Close()
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		inWrite.Close()
		return nil, ErrStart
	}
	defer outWrite.Close()
	stderr, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		inWrite.Close()
		outRead.Close()
		return nil, ErrStart
	}
	defer stderr.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inRead, outWrite, stderr
	if ctx.Err() != nil {
		inWrite.Close()
		outRead.Close()
		return nil, contextError(ctx)
	}
	if err := cmd.Start(); err != nil {
		inWrite.Close()
		outRead.Close()
		return nil, ErrStart
	}
	p := &process{stdin: inWrite, stdout: outRead, cmd: cmd, stop: make(chan error, 1), done: make(chan struct{})}
	if _, err := waitable(cmd.Process.Pid); err != nil {
		// The freshly launched child has not been reaped by us. On systems
		// without waitid support, fail startup and clean up rather than
		// returning a transport with no functioning exit monitor.
		p.finish(ErrStart, err != syscall.ECHILD)
		return nil, ErrStart
	}
	go p.supervise(ctx)
	return &Process{impl: p}, nil
}

func canonical(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	return err == nil && resolved == path
}

func privateDir(path string) bool {
	if !canonical(path) {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func within(parent, child string) bool {
	return child == parent || strings.HasPrefix(child, parent+string(os.PathSeparator))
}

func verifyExecutable(ctx context.Context, cfg Config) error {
	want, err := hex.DecodeString(cfg.SHA256)
	if err != nil || len(want) != sha256.Size || !canonical(cfg.Executable) {
		return ErrExecutable
	}
	f, err := os.Open(cfg.Executable)
	if err != nil {
		return ErrExecutable
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0111 == 0 || before.Mode().Perm()&0022 != 0 || before.Size() > 1<<30 {
		return ErrExecutable
	}
	hash := sha256.New()
	buf := make([]byte, 64<<10)
	var total int64
	for {
		if ctx.Err() != nil {
			return contextError(ctx)
		}
		n, readErr := f.Read(buf)
		total += int64(n)
		if total > 1<<30 {
			return ErrExecutable
		}
		_, _ = hash.Write(buf[:n])
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return ErrExecutable
		}
	}
	after, err := os.Stat(cfg.Executable)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(cfg.SHA256) {
		return ErrExecutable
	}
	return nil
}

// waitable never reaps the child: its PID (and hence our numeric PGID) remains
// reserved until group kill completes. Only this package may wait on this child.
// The caller must not ignore SIGCHLD/use SA_NOCLDWAIT or run an external reaper.
func waitable(pid int) (bool, error) {
	// Linux and Darwin amd64/arm64 siginfo_t start with three int32 fields:
	// signo, errno, code. Storage is aligned and larger than either ABI's struct.
	var info [32]uint64
	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, 1 /* P_PID */, uintptr(pid), uintptr(unsafe.Pointer(&info[0])), syscall.WEXITED|syscall.WNOWAIT|syscall.WNOHANG, 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return false, errno
		}
		code := (*[64]int32)(unsafe.Pointer(&info[0]))[2]
		// Darwin has historically returned stopped children despite WEXITED.
		return code >= 1 && code <= 3 /* CLD_EXITED/KILLED/DUMPED */, nil
	}
}

func (p *process) supervise(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	reason := ErrExited
	for {
		exited, err := waitable(p.cmd.Process.Pid)
		if err != nil {
			// ECHILD means ownership was lost; never signal that numeric group.
			// Other monitor failures stop the still-unreaped owned child.
			p.finish(ErrCleanup, err != syscall.ECHILD)
			return
		}
		if exited {
			break
		}
		select {
		case reason = <-p.stop:
			p.finish(reason, true)
			return
		case <-ctx.Done():
			p.finish(contextError(ctx), true)
			return
		case <-ticker.C:
		}
	}
	p.finish(reason, true)
}

func (p *process) finish(reason error, owned bool) {
	pid := p.cmd.Process.Pid
	// Setpgid with Pgid=0 creates a group whose ID is the new child's PID.
	// Guard even impossible IDs; never signal our own group or group 0/-1.
	if owned && pid > 1 && pid != syscall.Getpgrp() {
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			p.cleanup = ErrCleanup
			_ = p.cmd.Process.Kill() // direct leader fallback, no raw error output
		}
	} else {
		p.cleanup = ErrCleanup
	}
	// Signal before closing pipes. Closing stdout/stdin first can itself make
	// a live child exit (SIGPIPE/EOF), leaving a zombie-only Darwin group whose
	// kill returns EPERM and falsely classifies intentional stop as uncertain.
	// Both pipe ends are still closed before Wait, unblocking concurrent IO.
	p.closeIO()
	// No group signal occurs after this reap: that would permit PGID reuse.
	if err := p.cmd.Wait(); err != nil {
		var exited *exec.ExitError
		if !errors.As(err, &exited) {
			p.cleanup = ErrCleanup
		}
	}
	if p.cleanup != nil {
		reason = p.cleanup
	}
	p.terminal = reason
	close(p.done)
}

func (p *process) closeIO() {
	p.closePipes.Do(func() { p.stdin.Close(); p.stdout.Close() })
}

func (p *process) requestStop(reason error) {
	// The lifecycle goroutine owns teardown order: group signal, IO close,
	// then leader reap. A stop request must not trigger child exit ahead of it.
	select {
	case p.stop <- reason:
	default:
	}
}

func (p *process) read(b []byte) (int, error) {
	n, err := p.stdout.Read(b)
	if err != nil {
		p.requestStop(ErrTransport)
		if err == io.EOF {
			return n, io.EOF
		}
		return n, ErrTransport
	}
	return n, nil
}

func (p *process) write(b []byte) (int, error) {
	n, err := p.stdin.Write(b)
	if err != nil {
		p.requestStop(ErrTransport)
		return n, ErrTransport
	}
	return n, nil
}

func (p *process) close() error {
	p.requestStop(ErrClosed)
	<-p.done
	return p.cleanup
}

func (p *process) doneChan() <-chan struct{} { return p.done }
func (p *process) err() error {
	select {
	case <-p.done:
		return p.terminal
	default:
		return nil
	}
}

package researchexecute

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

// T02-pinned isolated headless-shell digest (versions.txt). The executor
// refuses to render with any other build: the receipt's executor identity is
// only meaningful when the binary is the pinned one.
const pinnedChromeSHA256 = "a0bfe7b4da4787b66058477d696cd1d09065d25f06a548947722b9af77ee8282"

var (
	errBackendNotConfigured = errors.New("backend binary not configured")
	errBackendUntrusted     = errors.New("backend binary does not match its pin")
)

// backends verifies subprocess binaries and reports their identities for
// receipts. Verification is lazy per kind (construction stays hermetic) and
// cached per executor (config is fixed at construction).
type backends struct {
	chromePath     string
	expectedChrome string
	pythonPath     string
	versionTimeout time.Duration

	mu       sync.Mutex
	chromeID *researchcontract.ExecutorIdentity
	pythonID *researchcontract.ExecutorIdentity
}

func (b *backends) checkExecutable(path, what string) error {
	if path == "" {
		return fmt.Errorf("%w: %s", errBackendNotConfigured, what)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s backend %q: %w", what, path, errBackendNotConfigured)
	}
	if info.IsDir() || info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("%s backend %q is not executable: %w", what, path, errBackendNotConfigured)
	}
	return nil
}

// chromeIdentity verifies the headless-shell digest pin and reports its
// version. Any mismatch fails closed: an unpinned browser never renders.
func (b *backends) chromeIdentity(ctx context.Context) (researchcontract.ExecutorIdentity, error) {
	b.mu.Lock()
	if b.chromeID != nil {
		defer b.mu.Unlock()
		return *b.chromeID, nil
	}
	b.mu.Unlock()

	if err := b.checkExecutable(b.chromePath, "browser"); err != nil {
		return researchcontract.ExecutorIdentity{}, err
	}
	digest, err := sha256File(b.chromePath)
	if err != nil {
		return researchcontract.ExecutorIdentity{}, err
	}
	want := b.expectedChrome
	if want == "" {
		want = pinnedChromeSHA256
	}
	if digest != strings.ToLower(want) {
		return researchcontract.ExecutorIdentity{}, fmt.Errorf("%w: headless shell digest %s... != pin %s...",
			errBackendUntrusted, digest[:12], want[:12])
	}
	version, err := runVersion(ctx, b.versionTimeout, b.chromePath, "--version")
	if err != nil {
		return researchcontract.ExecutorIdentity{}, err
	}
	id := researchcontract.ExecutorIdentity{Backend: BackendBrowser, Version: version, Digest: "sha256:" + digest}
	b.mu.Lock()
	b.chromeID = &id
	b.mu.Unlock()
	return id, nil
}

// pythonIdentity reports the exec runtime version.
func (b *backends) pythonIdentity(ctx context.Context) (researchcontract.ExecutorIdentity, error) {
	b.mu.Lock()
	if b.pythonID != nil {
		defer b.mu.Unlock()
		return *b.pythonID, nil
	}
	b.mu.Unlock()

	if err := b.checkExecutable(b.pythonPath, "exec"); err != nil {
		return researchcontract.ExecutorIdentity{}, err
	}
	version, err := runVersion(ctx, b.versionTimeout, b.pythonPath, "--version")
	if err != nil {
		return researchcontract.ExecutorIdentity{}, err
	}
	id := researchcontract.ExecutorIdentity{Backend: BackendExec, Version: version}
	b.mu.Lock()
	b.pythonID = &id
	b.mu.Unlock()
	return id, nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func runVersion(ctx context.Context, timeout time.Duration, path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("backend version probe failed: %w", err)
	}
	version := strings.TrimSpace(out.String())
	if version == "" {
		return "", errors.New("backend version probe returned no output")
	}
	if len(version) > 200 {
		version = version[:200]
	}
	return version, nil
}

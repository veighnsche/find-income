import { existsSync, readdirSync, statSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { chromium } from 'playwright-core';

// Shared browser entrypoint for the UI smoke suites (lane A). Every launch
// stays silent: a fresh temporary profile (Playwright's default) plus a
// mock credential store, so the run never touches the macOS keychain or the
// owner's browser profile. System Chrome is never used unless explicitly
// allowed, because it couples to the owner's profile.
const silentArgs = [
  '--password-store=basic',
  '--use-mock-keychain',
  '--no-first-run',
  '--no-default-browser-check',
  '--disable-sync',
];

export function keychainDir() {
  return join(homedir(), 'Library', 'Keychains');
}

export function snapshotKeychains() {
  const out = new Map();
  let names = [];
  try {
    names = readdirSync(keychainDir());
  } catch {
    return out;
  }
  for (const name of names) {
    try {
      const st = statSync(join(keychainDir(), name));
      out.set(name, `${st.size}:${st.mtimeMs}`);
    } catch {
      /* Entry vanished mid-snapshot; the after-pass reports drift. */
    }
  }
  return out;
}

export function assertKeychainsUntouched(before) {
  const after = snapshotKeychains();
  const touched = [];
  for (const [name, signature] of before) {
    if (after.get(name) !== signature) touched.push(name);
  }
  for (const name of after.keys()) {
    if (!before.has(name)) touched.push(`${name} (new)`);
  }
  if (touched.length > 0) {
    throw new Error(`macOS keychain touched during browser run: ${touched.join(', ')}`);
  }
}

function cachedHeadlessShell() {
  let names = [];
  try {
    names = readdirSync(join(homedir(), '.cache/ms-playwright'));
  } catch {
    return '';
  }
  const hits = names.filter((name) => name.startsWith('chromium_headless_shell-')).sort().reverse();
  for (const hit of hits) {
    for (const suffix of [
      'chrome-headless-shell-mac-arm64/chrome-headless-shell',
      'chrome-linux/headless_shell',
    ]) {
      const full = join(homedir(), '.cache/ms-playwright', hit, suffix);
      if (existsSync(full)) return full;
    }
  }
  return '';
}

export function resolveBrowserExecutable() {
  if (process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH) {
    return process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH;
  }
  return cachedHeadlessShell() || undefined;
}

export async function launchSilentBrowser() {
  const executablePath = resolveBrowserExecutable();
  if (!executablePath && process.env.JOBSEEK_ALLOW_SYSTEM_CHROME === '1') {
    return chromium.launch({ channel: 'chrome', headless: true, args: silentArgs });
  }
  return chromium.launch({
    ...(executablePath ? { executablePath } : {}),
    headless: true,
    args: silentArgs,
  });
}

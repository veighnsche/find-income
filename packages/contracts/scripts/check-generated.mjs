import { execFileSync } from 'node:child_process';
import { readFileSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const dir = mkdtempSync(join(tmpdir(), 'jobseek-ts-generated-'));
const output = join(dir, 'api.ts');
try {
  execFileSync('openapi-typescript', ['openapi.yaml', '-o', output], {
    cwd: process.cwd(),
    stdio: 'inherit',
  });
  const expected = readFileSync('src/generated/api.ts');
  const actual = readFileSync(output);
  if (!expected.equals(actual)) {
    throw new Error('Generated TypeScript types differ; run pnpm generate.');
  }
} finally {
  rmSync(dir, { recursive: true, force: true });
}

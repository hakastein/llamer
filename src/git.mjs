import { execFileSync } from 'node:child_process';

// Вывод git на крупном изменении не влезает в дефолтный буфер execFileSync.
export const GIT_MAX_BUFFER = 256 * 1024 * 1024;

export function git(args, cwd, env = {}) {
  return execFileSync('git', args, {
    cwd,
    encoding: 'utf-8',
    maxBuffer: GIT_MAX_BUFFER,
    stdio: ['ignore', 'pipe', 'pipe'],
    env: { ...process.env, ...env },
  }).trim();
}

export function tryGit(args, cwd) {
  try {
    return git(args, cwd);
  } catch {
    return null;
  }
}

export function repoRoot(cwd) {
  return git(['rev-parse', '--show-toplevel'], cwd);
}

export function isAncestor(sha, descendant, cwd) {
  return tryGit(['merge-base', '--is-ancestor', sha, descendant], cwd) !== null;
}

export function currentHead(cwd) {
  return git(['rev-parse', 'HEAD'], cwd);
}

export function firstLine(error) {
  return String(error.stderr || error.message).trim().split('\n')[0];
}

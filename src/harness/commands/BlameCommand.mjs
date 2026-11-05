import { execFileSync } from 'node:child_process';
import { Command } from '../Command.mjs';
import { Refusal } from '../../domain/Refusal.mjs';

export class BlameCommand extends Command {
  get name() {
    return 'blame';
  }

  get summary() {
    return 'who last changed a line and with what commit message';
  }

  get prints() {
    return true;
  }

  get options() {
    return [
      { flags: '--file <path>', hint: 'a repo-root-relative path', required: true },
      { flags: '--line <number>', hint: 'the line in the current version', required: true },
    ];
  }

  build(values) {
    let output;
    try {
      output = execFileSync('git', [
        'blame', '-L', `${values.line},${values.line}`, '--porcelain', '--', values.file,
      ], { encoding: 'utf-8', stdio: ['ignore', 'pipe', 'pipe'] });
    } catch (error) {
      throw new Refusal(`git blame не ответил по ${values.file}:${values.line} (${error.message.split('\n')[0]})`);
    }
    const field = (name) => (new RegExp(`^${name} (.+)$`, 'm').exec(output)?.[1] ?? '');
    const seconds = Number(field('author-time'));
    const date = Number.isFinite(seconds) ? new Date(seconds * 1000).toISOString().slice(0, 10) : '';
    return { print: `${output.slice(0, 8)} ${field('author')} ${date} — ${field('summary')}` };
  }
}

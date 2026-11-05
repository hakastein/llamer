import { Command } from '../Command.mjs';
import { Refusal } from '../../domain/Refusal.mjs';
import { Finding, SEVERITIES, parseRelated } from '../../domain/Finding.mjs';

export class FindingCommand extends Command {
  get name() {
    return 'finding';
  }

  get summary() {
    return 'record a remark on the request, anchored to diff lines';
  }

  get manual() {
    return 'Highlight everything the remark is about — the whole condition, call, branch or'
      + ' method; that is where the thread anchors.\n'
      + 'For DELETED code use --deleted, anchored on the old side; otherwise the thread lands'
      + ' on the new side, on whatever replaced it.';
  }

  get options() {
    return [
      {
        flags: '--file <path...>',
        hint: 'a repo-root-relative path, as in the diff',
        required: true,
      },
      {
        flags: '--line <number>',
        hint: 'first line of the fragment',
        required: true,
      },
      {
        flags: '--line-end <number>',
        hint: 'last line of it; no default, so repeat --line for a one-line remark',
        required: true,
      },
      {
        flags: '--text <text>',
        hint: 'what is wrong and how to solve it',
        required: true,
      },
      {
        flags: '--severity <level>', hint: 'the level the consequence puts the finding on', required: true, choices: SEVERITIES,
      },
      {
        flags: '--severity-reason <text>',
        hint: 'the one consequence that puts it there, distinct from --text',
        required: true,
      },
      {
        flags: '--related <list>',
        hint: 'what the remark rests on: «path», «path:line» or «path:line-line»,'
          + ' comma-separated; anything else in the value is dropped silently. Lines are'
          + ' from the new version even with --deleted',
      },
      { flags: '--trace <text>', hint: 'the chain of reasoning, for a finding about interaction' },
      {
        flags: '--suggestion <code>',
        hint: 'replaces the WHOLE fragment, any length; the button applies it unread, so'
          + ' offer one only when you know the whole fix. Markup is added for you',
      },
      {
        flags: '--deleted',
        hint: 'the fragment is deleted lines: --line/--line-end are from the version'
          + ' BEFORE the request (git show <base>:<file>)',
      },
    ];
  }

  build(values, context) {
    const related = parseRelated(values.related);
    if (context.relatedRequired && related.length === 0) {
      throw new Refusal('нужен --related: куда ты смотрел, устанавливая находку'
        + ' («путь:строка» через запятую). Если находка очевидна по одной строке,'
        + ' назови эту строку.');
    }

    const finding = new Finding({
      file: Array.isArray(values.file) ? values.file[0] : values.file,
      line: values.line,
      lineEnd: values.lineEnd,
      deleted: Boolean(values.deleted),
      text: values.text,
      severity: values.severity,
      severityReason: values.severityReason,
      suggestion: values.suggestion ?? null,
      related,
      trace: values.trace ?? null,
    });
    finding.assertAnchored(context.diff);
    if (finding.suggestion !== null) {
      finding.assertSuggestionApplies(context.readFileLines(finding.file), finding.suggestion);
    }
    const event = finding.toEvent();
    return {
      events: [event],
      confirmation: ` ${event.file}:${event.line}-${event.lineEnd}`,
    };
  }
}

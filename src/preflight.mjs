import { join, resolve } from 'node:path';
import {
  CONFIG_FILE, ConfigError, loadConfig, driversInUse, skillsInUse,
} from './config.mjs';
import { driverGaps } from './drivers/index.mjs';
import { GITLAB_URL_ENV, parseMrRef } from './gitlab/MergeRequest.mjs';
import { SkillLibrary } from './stages/SkillLibrary.mjs';
import { repoRoot } from './git.mjs';

const gap = (id, message) => ({ id, message });

function gitlabGaps(options, env) {
  const gaps = [];
  if (!env.GITLAB_TOKEN) gaps.push(gap('gitlab-token', 'не задан GITLAB_TOKEN — merge request не прочитать и не опубликовать'));
  try {
    if (!parseMrRef(options.mr, env).url) {
      gaps.push(gap('gitlab-url', `адрес GitLab неизвестен: дай ссылку на MR целиком или задай ${GITLAB_URL_ENV.join(' или ')}`));
    }
  } catch (error) {
    gaps.push(gap('mr-ref', error.message));
  }
  return gaps;
}

/**
 * Проверяет только своё: git, драйверы этапов и их вход, токен GitLab при
 * GitLab-входе или публикации и наличие скиллов. Софт скиллов, навигацию и
 * docker готовит запускающий — их здесь нет. Любой пробел останавливает прогон.
 */
export function checkReadiness({ cwd, options, env }) {
  let root;
  try {
    root = repoRoot(cwd);
  } catch (error) {
    return { gaps: [gap('git', `git не нашёл репозиторий в ${cwd} (${String(error.stderr || error.message).trim().split('\n')[0]})`)] };
  }

  let config;
  try {
    config = loadConfig(options.config ? resolve(cwd, options.config) : join(root, CONFIG_FILE), env);
  } catch (error) {
    if (!(error instanceof ConfigError)) throw error;
    return { root, gaps: [gap('config', error.message)] };
  }

  const gaps = [];
  for (const name of driversInUse(config)) gaps.push(...driverGaps(name, config.drivers[name], env));
  if (options.input === 'gitlab') gaps.push(...gitlabGaps(options, env));

  let library = null;
  try {
    library = SkillLibrary.load(config.skillPaths);
    const missing = library.missing(skillsInUse(config));
    if (missing.length > 0) {
      gaps.push(gap('skills', `скиллы не найдены в skillPaths (${config.skillPaths.join(', ') || 'пусто'}): ${missing.join(', ')}`));
    }
  } catch (error) {
    gaps.push(gap('skills', error.message));
  }

  return {
    root, config, library, gaps,
  };
}

export function formatGaps(gaps) {
  return gaps.map((item) => `- ${item.message}`).join('\n');
}

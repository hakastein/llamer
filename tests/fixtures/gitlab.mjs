import { createServer } from 'node:http';

/**
 * Заглушка GitLab REST API на локальном HTTP-сервере: отвечает тем, что дали
 * тесты, запись отвечает созданной сущностью, каждый запрос остаётся в `requests`.
 * Привязанный к строкам тред возвращается DiffNote — так делает GitLab.
 */
export async function startGitlab({
  project, iid, token, mr, changes, versions, discussions = [],
}) {
  const requests = [];
  const base = `/api/v4/projects/${encodeURIComponent(project)}/merge_requests/${iid}`;
  let created = 0;

  const server = createServer((req, res) => {
    let raw = '';
    req.on('data', (chunk) => { raw += chunk; });
    req.on('end', () => {
      const url = new URL(req.url, 'http://stub');
      const body = raw ? JSON.parse(raw) : null;
      requests.push({
        method: req.method, path: url.pathname, query: url.search, token: req.headers['private-token'], body,
      });
      const reply = (status, data) => {
        res.writeHead(status, { 'Content-Type': 'application/json' });
        res.end(data === undefined ? '' : JSON.stringify(data));
      };
      if (req.headers['private-token'] !== token) return reply(401, { message: '401 Unauthorized' });

      const sub = url.pathname.startsWith(base) ? url.pathname.slice(base.length) : null;
      if (sub === null) return reply(404, { message: '404 Not Found' });
      if (req.method === 'GET' && sub === '') return reply(200, mr);
      if (req.method === 'GET' && sub === '/changes') return reply(200, { changes });
      if (req.method === 'GET' && sub === '/versions') return reply(200, versions);
      if (req.method === 'GET' && sub === '/discussions') {
        return reply(200, url.searchParams.get('page') === '1' ? discussions : []);
      }
      if (req.method === 'POST' && sub === '/discussions') {
        created += 1;
        return reply(201, {
          id: `new-${created}`,
          notes: [{ id: created, type: body.position ? 'DiffNote' : 'DiscussionNote', position: body.position ?? null }],
        });
      }
      if (req.method === 'POST' && sub === '/notes') return reply(201, { id: 1000 + requests.length });
      if (req.method === 'PUT' && sub.startsWith('/discussions/')) return reply(200, { resolved: true });
      if (req.method === 'DELETE' && sub.startsWith('/notes/')) return reply(204);
      return reply(404, { message: '404 Not Found' });
    });
  });
  await new Promise((resolve) => { server.listen(0, '127.0.0.1', resolve); });
  return {
    url: `http://127.0.0.1:${server.address().port}`,
    requests,
    close: () => new Promise((resolve) => { server.close(resolve); }),
  };
}

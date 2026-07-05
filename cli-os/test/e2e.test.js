// End-to-end: boot the real server against the built-in mock upstream and drive the full path
// (auth -> route -> memory -> reserve -> call -> meter -> commit -> ledger) over real HTTP.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

let server, db, base, home, repoDir;
let tokenDemo, tokenCapped;

before(async () => {
  home = fs.mkdtempSync(path.join(os.tmpdir(), 'cliose2e-'));
  process.env.LOOPRITE_HOME = home;
  delete process.env.LOOPRITE_MASTER_KEY;
  const { loadConfig, ensureHome } = await import('../src/config.js');
  const { openDb } = await import('../src/state/db.js');
  const { ensureMasterKey } = await import('../src/security/vault.js');
  const { mintToken } = await import('../src/security/tokens.js');
  const { buildServer } = await import('../src/server.js');
  const { nowISO } = await import('../src/util.js');

  const cfg = loadConfig(); ensureHome(cfg); ensureMasterKey(cfg.masterKeyPath);
  db = openDb(cfg.dbPath);

  // mock provider (no key, no network) as default
  db.prepare(`INSERT INTO providers(name,adapter,base_url,enc_key,enabled,is_default,created_at) VALUES('mock','mock',NULL,NULL,1,1,?)`).run(nowISO());

  // a repo with memory
  repoDir = fs.mkdtempSync(path.join(os.tmpdir(), 'e2erepo-'));
  fs.mkdirSync(path.join(repoDir, '.l00prite'), { recursive: true });
  fs.writeFileSync(path.join(repoDir, '.l00prite', 'constraints.md'), '# Constraints\nUse tabs.');
  db.prepare(`INSERT INTO repos(id,root,project,created_at) VALUES('demo',?,'demo',?)`).run(repoDir, nowISO());

  tokenDemo = mintToken(db, { project: 'demo', repo: 'demo' }).token;
  const capped = mintToken(db, { project: 'capped' });
  tokenCapped = capped.token;
  db.prepare(`INSERT OR REPLACE INTO caps(project,window,limit_usd) VALUES('capped','daily',0.10)`).run();

  server = buildServer({ db, cfg, aliases: {} });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  base = `http://127.0.0.1:${server.address().port}`;
});

after(() => {
  server?.close();
  try { fs.rmSync(home, { recursive: true, force: true }); fs.rmSync(repoDir, { recursive: true, force: true }); } catch { /* ignore */ }
});

test('rejects missing token with 401', async () => {
  const r = await fetch(`${base}/v1/chat/completions`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ messages: [{ role: 'user', content: 'hi' }] }) });
  assert.equal(r.status, 401);
});

test('non-streaming chat completion returns OpenAI-shaped response + records ledger', async () => {
  const r = await fetch(`${base}/v1/chat/completions`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', authorization: `Bearer ${tokenDemo}` },
    body: JSON.stringify({ model: 'demo-model', messages: [{ role: 'user', content: 'hello there' }] }),
  });
  assert.equal(r.status, 200);
  const j = await r.json();
  assert.equal(j.object, 'chat.completion');
  assert.ok(j.choices[0].message.content.length > 0);
  assert.ok(j.usage.completion_tokens > 0);
  const reqId = r.headers.get('x-l00prite-request-id');
  const row = db.prepare(`SELECT * FROM ledger WHERE request_id = ?`).get(reqId);
  assert.equal(row.outcome, 'ok');
  assert.equal(row.provider, 'mock');
  assert.equal(row.memory_status, 'ok', 'memory should have been injected from the repo');
});

test('streaming chat completion emits SSE deltas then [DONE]', async () => {
  const r = await fetch(`${base}/v1/chat/completions`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', authorization: `Bearer ${tokenDemo}` },
    body: JSON.stringify({ model: 'demo-model', stream: true, messages: [{ role: 'user', content: 'stream please' }] }),
  });
  assert.equal(r.status, 200);
  assert.match(r.headers.get('content-type') || '', /text\/event-stream/);
  const text = await r.text();
  assert.ok(text.includes('data: [DONE]'));
  const contentChunks = text.split('\n\n').filter((b) => b.startsWith('data:') && !b.includes('[DONE]'))
    .map((b) => { try { return JSON.parse(b.slice(5).trim()); } catch { return null; } })
    .filter(Boolean);
  const joined = contentChunks.map((c) => c.choices?.[0]?.delta?.content || '').join('');
  assert.ok(joined.length > 0, 'stream should carry content deltas');
});

test('cost cap denies with 402 before spending', async () => {
  // reservation ceiling for an unknown-price mock model is $0.25 > the $0.10 cap -> denied.
  const r = await fetch(`${base}/v1/chat/completions`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', authorization: `Bearer ${tokenCapped}` },
    body: JSON.stringify({ model: 'demo-model', messages: [{ role: 'user', content: 'hi' }] }),
  });
  assert.equal(r.status, 402);
  const j = await r.json();
  assert.equal(j.error.code, 'cost_cap');
});

test('repo-scoped token cannot be widened to another repo via header (403)', async () => {
  const r = await fetch(`${base}/v1/chat/completions`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', authorization: `Bearer ${tokenDemo}`, 'x-l00prite-repo': 'some-other-repo' },
    body: JSON.stringify({ model: 'demo', messages: [{ role: 'user', content: 'hi' }] }),
  });
  assert.equal(r.status, 403);
});

test('unregistered repo returns 404', async () => {
  const r = await fetch(`${base}/v1/chat/completions`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', authorization: `Bearer ${tokenCapped}`, 'x-l00prite-repo': 'does-not-exist' },
    body: JSON.stringify({ model: 'demo', messages: [{ role: 'user', content: 'hi' }] }),
  });
  assert.equal(r.status, 404);
});

test('/v1/models and /healthz respond', async () => {
  const h = await fetch(`${base}/healthz`); assert.equal(h.status, 200);
  const hj = await h.json(); assert.equal(hj.status, 'ok'); assert.ok(hj.providers.find((p) => p.name === 'mock'));
  const m = await fetch(`${base}/v1/models`); assert.equal(m.status, 200);
});

import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

function tmpHome() {
  const d = fs.mkdtempSync(path.join(os.tmpdir(), 'cliostest-'));
  process.env.LOOPRITE_HOME = d;
  delete process.env.LOOPRITE_MASTER_KEY;
  return d;
}

test('vault: encrypt/decrypt roundtrip, ciphertext hides plaintext', async () => {
  const home = tmpHome();
  const { loadConfig, ensureHome } = await import('../src/config.js');
  const { ensureMasterKey, encryptSecret, decryptSecret } = await import('../src/security/vault.js');
  const cfg = loadConfig(); ensureHome(cfg); ensureMasterKey(cfg.masterKeyPath);
  const secret = 'sk-super-secret-123';
  const blob = encryptSecret(cfg, secret);
  assert.ok(!blob.includes(secret), 'ciphertext must not contain plaintext');
  assert.equal(decryptSecret(cfg, blob), secret);
  assert.equal(fs.statSync(cfg.masterKeyPath).mode & 0o777, 0o600, 'master key must be 0600');
  fs.rmSync(home, { recursive: true, force: true });
});

test('tokens: mint/verify, reject wrong secret, reject revoked', async () => {
  tmpHome();
  const { loadConfig } = await import('../src/config.js');
  const { openDb } = await import('../src/state/db.js');
  const { mintToken, verifyToken, revokeToken } = await import('../src/security/tokens.js');
  const db = openDb(loadConfig().dbPath);
  const { id, token } = mintToken(db, { project: 'demo', repo: 'r1' });
  const p = verifyToken(db, token);
  assert.equal(p.project, 'demo'); assert.equal(p.repo, 'r1');
  assert.equal(verifyToken(db, `l00p_${id}_wrongsecret`), null);
  assert.equal(verifyToken(db, 'garbage'), null);
  revokeToken(db, id);
  assert.equal(verifyToken(db, token), null, 'revoked token must fail');
});

test('pep: reserve respects daily cap; commit/refund adjust spend', async () => {
  tmpHome();
  const { loadConfig } = await import('../src/config.js');
  const { openDb } = await import('../src/state/db.js');
  const pep = await import('../src/policy/pep.js');
  const db = openDb(loadConfig().dbPath);
  db.prepare(`INSERT OR REPLACE INTO caps(project,window,limit_usd) VALUES('p','daily',1.0)`).run();

  const a = pep.reserve(db, { project: 'p', amountUsd: 0.6, defaultCap: 10 });
  assert.equal(a.ok, true);
  const b = pep.reserve(db, { project: 'p', amountUsd: 0.6, defaultCap: 10 });
  assert.equal(b.ok, false, 'second reserve should breach the $1 cap');
  assert.equal(b.reason, 'cost_cap');

  pep.refund(db, a.reservationId);
  const c = pep.reserve(db, { project: 'p', amountUsd: 0.6, defaultCap: 10 });
  assert.equal(c.ok, true, 'after refund, budget frees up');
  pep.commit(db, c.reservationId, 0.42);
  const s = pep.getSpend(db, 'p', 10);
  assert.ok(Math.abs(s.committed - 0.42) < 1e-9);
  assert.ok(Math.abs(s.reserved) < 1e-9);
});

test('meter: known Anthropic price computes; unknown provider is flagged estimated', async () => {
  tmpHome();
  const { costOf } = await import('../src/gateway/meter.js');
  const c = costOf('anthropic', 'claude-opus-4-8', { prompt_tokens: 1_000_000, completion_tokens: 1_000_000, cache_read_tokens: 0, cache_write_tokens: 0 });
  assert.ok(c.priced);
  assert.ok(Math.abs(c.usd - 30.0) < 1e-6, `opus-4-8 1M in + 1M out = $30, got ${c.usd}`);
  const u = costOf('nonesuch', 'whatever', { prompt_tokens: 100, completion_tokens: 100 });
  assert.equal(u.priced, false);
  assert.equal(u.estimated, true);
});

test('anthropic adapter: request translation (system-as-field, input_schema tools, max_tokens)', async () => {
  const a = await import('../src/gateway/adapters/anthropic.js');
  const req = {
    model: 'x', max_tokens: 256,
    messages: [{ role: 'system', content: 'be terse' }, { role: 'user', content: 'hi' }],
    tools: [{ type: 'function', function: { name: 'get_weather', description: 'w', parameters: { type: 'object', properties: { q: { type: 'string' } } } } }],
    tool_choice: 'auto',
  };
  const body = a.buildRequest({ model: 'claude-x', openaiReq: req, stream: false });
  assert.equal(body.system, 'be terse', 'system must be a top-level field');
  assert.equal(body.max_tokens, 256);
  assert.equal(body.messages.length, 1, 'system message must not remain in messages');
  assert.equal(body.tools[0].name, 'get_weather');
  assert.ok(body.tools[0].input_schema, 'tools use input_schema, not function wrapper');
  assert.deepEqual(body.tool_choice, { type: 'auto' });
});

test('anthropic adapter: SSE events fold into OpenAI chunks + usage', async () => {
  const a = await import('../src/gateway/adapters/anthropic.js');
  const st = a.newStreamState('claude-x');
  const events = [
    { data: JSON.stringify({ type: 'message_start', message: { usage: { input_tokens: 10, output_tokens: 0 } } }) },
    { data: JSON.stringify({ type: 'content_block_start', index: 0, content_block: { type: 'text' } }) },
    { data: JSON.stringify({ type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: 'Hello' } }) },
    { data: JSON.stringify({ type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: ' world' } }) },
    { data: JSON.stringify({ type: 'message_delta', delta: { stop_reason: 'end_turn' }, usage: { output_tokens: 2 } }) },
    { data: JSON.stringify({ type: 'message_stop' }) },
  ];
  let text = '', done = false, usage = null;
  for (const ev of events) {
    const out = a.onEvent(st, ev);
    for (const ch of out.deltas || []) { const d = ch.choices[0].delta; if (d.content) text += d.content; }
    if (out.usage) usage = out.usage;
    if (out.done) done = true;
  }
  assert.equal(text, 'Hello world');
  assert.equal(done, true);
  assert.equal(usage.prompt_tokens, 10);
  assert.equal(usage.completion_tokens, 2);
});

test('pep: rejects non-finite / negative reservation amounts (fail closed)', async () => {
  tmpHome();
  const { loadConfig } = await import('../src/config.js');
  const { openDb } = await import('../src/state/db.js');
  const pep = await import('../src/policy/pep.js');
  const db = openDb(loadConfig().dbPath);
  assert.equal(pep.reserve(db, { project: 'p', amountUsd: NaN, defaultCap: 10 }).ok, false);
  assert.equal(pep.reserve(db, { project: 'p', amountUsd: -5, defaultCap: 10 }).ok, false);
  assert.equal(pep.reserve(db, { project: 'p', amountUsd: Infinity, defaultCap: 10 }).ok, false);
});

test('config: malformed default cap falls back safe (never NaN)', async () => {
  tmpHome();
  process.env.LOOPRITE_DEFAULT_DAILY_CAP = 'not-a-number';
  const { loadConfig } = await import('../src/config.js');
  assert.equal(loadConfig().defaultDailyCapUsd, 10);
  delete process.env.LOOPRITE_DEFAULT_DAILY_CAP;
});

test('meter: reservation ceiling scales with max_tokens (bounds output cost)', async () => {
  tmpHome();
  const { reservationCeiling } = await import('../src/gateway/meter.js');
  const small = reservationCeiling('anthropic', 'claude-opus-4-8', { messages: [{ role: 'user', content: 'hi' }], max_tokens: 100 });
  const big = reservationCeiling('anthropic', 'claude-opus-4-8', { messages: [{ role: 'user', content: 'hi' }], max_tokens: 100000 });
  assert.ok(big > small * 10, 'a larger max_tokens must reserve a larger ceiling');
});

test('memory: symlink escaping the repo root is not read (containment)', async () => {
  const home = tmpHome();
  const memory = await import('../src/memory/memory.js');
  const outside = fs.mkdtempSync(path.join(os.tmpdir(), 'outside-'));
  fs.writeFileSync(path.join(outside, 'secret.txt'), 'TOP-SECRET-OUTSIDE-ROOT');
  const repo = fs.mkdtempSync(path.join(os.tmpdir(), 'repo-'));
  fs.mkdirSync(path.join(repo, '.l00prite'), { recursive: true });
  fs.writeFileSync(path.join(repo, '.l00prite', 'memory.md'), '# Memory\nlegit content');
  fs.symlinkSync(path.join(outside, 'secret.txt'), path.join(repo, '.l00prite', 'constraints.md'));
  const ctx = memory.query({ repoRoot: repo, requestDigest: {}, budgets: { contextTokens: 8000 }, options: {} });
  const joined = ctx.blocks.map((b) => b.text).join('\n');
  assert.ok(!joined.includes('TOP-SECRET-OUTSIDE-ROOT'), 'symlinked-out file must not be read');
  assert.ok(!ctx.blocks.some((b) => b.source_path.includes('constraints')), 'escaping symlink must be skipped');
  fs.rmSync(home, { recursive: true, force: true }); fs.rmSync(repo, { recursive: true, force: true }); fs.rmSync(outside, { recursive: true, force: true });
});

test('memory: reads .l00prite blocks; empty when no memory dir; containment', async () => {
  const home = tmpHome();
  const memory = await import('../src/memory/memory.js');
  const repo = fs.mkdtempSync(path.join(os.tmpdir(), 'repo-'));
  fs.mkdirSync(path.join(repo, '.l00prite'), { recursive: true });
  fs.writeFileSync(path.join(repo, '.l00prite', 'constraints.md'), '# Constraints\nNo secrets in memory.');
  const ctx = memory.query({ repoRoot: repo, requestDigest: { user_intent: 'add constraints check' }, budgets: { contextTokens: 8000 }, options: {} });
  assert.equal(ctx.status, 'ok');
  assert.equal(ctx.blocks[0].kind, 'constraint');
  assert.ok(ctx.blocks[0].text.includes('No secrets'));

  const empty = memory.query({ repoRoot: fs.mkdtempSync(path.join(os.tmpdir(), 'norepo-')), requestDigest: {}, budgets: {}, options: {} });
  assert.equal(empty.status, 'empty');
  fs.rmSync(home, { recursive: true, force: true }); fs.rmSync(repo, { recursive: true, force: true });
});

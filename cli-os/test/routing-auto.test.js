// Auto-routing unit tests: requirement derivation, capability filter, preference ordering, the
// unknown-price-last rule, and the typed error surfaces — all against the real anthropic/zhipu
// manifests so the facts are the shipped ones.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

function cfgWith(routingOverride) {
  const d = fs.mkdtempSync(path.join(os.tmpdir(), 'autocfg-'));
  process.env.LOOPRITE_HOME = d;
  if (routingOverride) fs.writeFileSync(path.join(d, 'config.json'), JSON.stringify({ routing: routingOverride }));
  return d;
}
const PROVIDERS = [
  { name: 'anthropic', enabled: 1, is_default: 1 },
  { name: 'zhipu', enabled: 1, is_default: 0 },
];
const mk = (model, extra = {}) => ({ model, messages: [{ role: 'user', content: 'write a function' }], ...extra });

test('deriveRequirements: tools, vision, context, streaming usage', async () => {
  cfgWith();
  const { deriveRequirements } = await import('../src/gateway/router-auto.js');
  const plain = deriveRequirements(mk('x'));
  assert.equal(plain.needs_tools, false);
  assert.equal(plain.needs_vision, false);
  const tooled = deriveRequirements(mk('x', { tools: [{ type: 'function', function: { name: 'f', parameters: {} } }] }));
  assert.equal(tooled.needs_tools, true);
  const vis = deriveRequirements({ messages: [{ role: 'user', content: [{ type: 'image_url', image_url: { url: 'data:image/png;base64,AA' } }] }] });
  assert.equal(vis.needs_vision, true);
  const su = deriveRequirements(mk('x', { stream: true, stream_options: { include_usage: true } }));
  assert.equal(su.needs_streaming_usage, true);
  assert.ok(deriveRequirements(mk('x', { max_tokens: 5000 })).min_context_tokens > deriveRequirements(mk('x', { max_tokens: 10 })).min_context_tokens);
});

test('auto:cheap prefers a confirmed price over a cheaper-but-unconfirmed one (no $0/unknown trap)', async () => {
  cfgWith();
  const { loadConfig } = await import('../src/config.js');
  const router = await import('../src/gateway/router.js');
  const cfg = loadConfig();
  const r = router.pick({ providers: PROVIDERS, aliases: {}, openaiReq: mk('auto:cheap'), cfg });
  // glm-5.2 has a cheaper reported input price but price_confidence "unconfirmed" (tier 1); a
  // confirmed-price Anthropic model (tier 0) must win despite the higher blended dollar figure.
  assert.equal(r.provider, 'anthropic');
  assert.equal(r.decision.rule_id, 'auto_select');
  const winner = r.decision.candidates[0];
  assert.equal(winner.price_tier, 0, 'winner must be a confirmed-price model');
  // and an unpriced model (tier 2) is never ranked first
  assert.ok(r.decision.candidates.every((c, i) => !(c.price_tier === 2 && i === 0)));
});

test('auto:quality picks the highest operator quality rank', async () => {
  cfgWith();
  const { loadConfig } = await import('../src/config.js');
  const router = await import('../src/gateway/router.js');
  const r = router.pick({ providers: PROVIDERS, aliases: {}, openaiReq: mk('auto:quality'), cfg: loadConfig() });
  assert.equal(`${r.provider}/${r.model}`, 'anthropic/claude-opus-4-8'); // rank 96 in defaults
  assert.match(r.decision.reason, /quality rank \(96/);
});

test('config quality-rank override beats the built-in default', async () => {
  cfgWith({ qualityRanks: { 'zhipu/glm-5.2': 99 } });
  const { loadConfig } = await import('../src/config.js');
  const router = await import('../src/gateway/router.js');
  const r = router.pick({ providers: PROVIDERS, aliases: {}, openaiReq: mk('auto:quality'), cfg: loadConfig() });
  assert.equal(r.provider, 'zhipu', 'the override (99) should outrank opus (96)');
});

test('capability filter drops non-vision models on an image request and logs reasons', async () => {
  cfgWith();
  const { loadConfig } = await import('../src/config.js');
  const router = await import('../src/gateway/router.js');
  const req = { model: 'auto:cheap', messages: [{ role: 'user', content: [{ type: 'text', text: 'what is this' }, { type: 'image_url', image_url: { url: 'data:image/png;base64,AA' } }] }] };
  const r = router.pick({ providers: PROVIDERS, aliases: {}, openaiReq: req, cfg: loadConfig() });
  assert.ok(r.decision.rejected.some((x) => x.target === 'zhipu/glm-5.2'), 'glm-5.2 (no vision) must be rejected');
  assert.ok(r.decision.candidates.every((c) => c.target !== 'zhipu/glm-5.2'), 'rejected model is not a candidate');
});

test('no capable model -> 400 no_capable_model with a denial decision', async () => {
  cfgWith({ profiles: { needsmagic: { require: ['magic'], preference: 'cost' } } });
  const { loadConfig } = await import('../src/config.js');
  const router = await import('../src/gateway/router.js');
  try {
    router.pick({ providers: PROVIDERS, aliases: {}, openaiReq: mk('auto:needsmagic'), cfg: loadConfig() });
    assert.fail('should have thrown');
  } catch (e) {
    assert.equal(e.status, 400);
    assert.equal(e.code, 'no_capable_model');
    assert.equal(e.decision.rule_id, 'auto_no_capable');
  }
});

test('all capable providers circuit-tripped -> 503', async () => {
  cfgWith();
  const { loadConfig } = await import('../src/config.js');
  const router = await import('../src/gateway/router.js');
  for (let i = 0; i < 3; i++) router.markFailure('anthropic');
  try {
    router.pick({ providers: [{ name: 'anthropic', enabled: 1, is_default: 1 }], aliases: {}, openaiReq: mk('auto:quality'), cfg: loadConfig() });
    assert.fail('should have thrown 503');
  } catch (e) {
    assert.equal(e.status, 503);
    assert.equal(e.code, 'providers_unavailable');
  } finally {
    router.markSuccess('anthropic'); // reset breaker for other tests
  }
});

test('unknown profile -> 400', async () => {
  cfgWith();
  const { loadConfig } = await import('../src/config.js');
  const router = await import('../src/gateway/router.js');
  try { router.pick({ providers: PROVIDERS, aliases: {}, openaiReq: mk('auto:bogus'), cfg: loadConfig() }); assert.fail('should throw'); }
  catch (e) { assert.equal(e.status, 400); assert.match(e.message, /Unknown auto profile/); }
});

test('explicit provider/model pin beats model=auto (operator intent wins)', async () => {
  cfgWith();
  const { loadConfig } = await import('../src/config.js');
  const router = await import('../src/gateway/router.js');
  const r = router.pick({ providers: PROVIDERS, aliases: {}, openaiReq: mk('auto:cheap'), routeHeader: 'anthropic/claude-opus-4-8', cfg: loadConfig() });
  assert.equal(r.decision.rule_id, 'explicit_pin');
  assert.equal(`${r.provider}/${r.model}`, 'anthropic/claude-opus-4-8');
});

test('auto selection is deterministic (stable tiebreak)', async () => {
  cfgWith();
  const { loadConfig } = await import('../src/config.js');
  const router = await import('../src/gateway/router.js');
  const cfg = loadConfig();
  const a = router.pick({ providers: PROVIDERS, aliases: {}, openaiReq: mk('auto:balanced'), cfg });
  const b = router.pick({ providers: PROVIDERS, aliases: {}, openaiReq: mk('auto:balanced'), cfg });
  assert.equal(`${a.provider}/${a.model}`, `${b.provider}/${b.model}`);
  assert.deepEqual(a.decision.candidates.map((c) => c.target), b.decision.candidates.map((c) => c.target));
});

test('no routing config -> auto is inert (model treated literally, no auto tier)', async () => {
  cfgWith();
  const router = await import('../src/gateway/router.js');
  // cfg omitted => routing null => auto disabled; falls through to default provider.
  const r = router.pick({ providers: PROVIDERS, aliases: {}, openaiReq: mk('auto:cheap') });
  assert.notEqual(r.decision.rule_id, 'auto_select');
});

// ---- regression tests for verified review findings ----

test('auto:cheap refuses to route to an UNPRICED model (400 no_priced_model, no unmetered spend)', async () => {
  cfgWith();
  const { loadConfig } = await import('../src/config.js');
  const router = await import('../src/gateway/router.js');
  // zhipu only + a vision request => only glm-5v-turbo is capable, and it is unpriced (tier 2).
  // The cost profile must refuse rather than pick an unpriceable model whose calls commit $0.
  const req = { model: 'auto:cheap', messages: [{ role: 'user', content: [{ type: 'image_url', image_url: { url: 'data:image/png;base64,AA' } }] }] };
  try {
    router.pick({ providers: [{ name: 'zhipu', enabled: 1, is_default: 1 }], aliases: {}, openaiReq: req, cfg: loadConfig() });
    assert.fail('should have refused');
  } catch (e) {
    assert.equal(e.status, 400);
    assert.equal(e.code, 'no_priced_model');
  }
});

test('capability filter rejects a model whose max_output is below the requested max_tokens', async () => {
  cfgWith();
  const { loadConfig } = await import('../src/config.js');
  const router = await import('../src/gateway/router.js');
  // haiku max_output is 64000; sonnet/opus/fable are 128000. Requesting 100000 must drop haiku.
  const r = router.pick({ providers: [{ name: 'anthropic', enabled: 1, is_default: 1 }], aliases: {}, openaiReq: { model: 'auto:cheap', max_tokens: 100000, messages: [{ role: 'user', content: 'hi' }] }, cfg: loadConfig() });
  assert.notEqual(r.model, 'claude-haiku-4-5', 'haiku cannot emit 100000 output tokens');
  assert.equal(r.model, 'claude-sonnet-5', 'cheapest of the 128k-output models');
  assert.ok(r.decision.rejected.some((x) => x.target === 'anthropic/claude-haiku-4-5' && /max_output/.test(x.reasons.join(''))));
});

test('a base64 image is not counted as prompt text (no context inflation)', async () => {
  cfgWith();
  const { deriveRequirements } = await import('../src/gateway/router-auto.js');
  const bigB64 = 'A'.repeat(200000); // a ~200k-char data URI
  const req = { messages: [{ role: 'user', content: [{ type: 'text', text: 'what is this' }, { type: 'image_url', image_url: { url: `data:image/png;base64,${bigB64}` } }] }] };
  const r = deriveRequirements(req, 4096);
  // Old behavior counted ~200000/3.5 ≈ 57000 prompt tokens; text-only + per-image constant is tiny.
  assert.ok(r.promptTokens < 4000, `image bytes must not inflate promptTokens, got ${r.promptTokens}`);
});

test('auto profile lookup is case-insensitive (AUTO:CHEAP resolves)', async () => {
  cfgWith();
  const { loadConfig } = await import('../src/config.js');
  const router = await import('../src/gateway/router.js');
  const r = router.pick({ providers: [{ name: 'anthropic', enabled: 1, is_default: 1 }], aliases: {}, openaiReq: mk('AUTO:CHEAP'), cfg: loadConfig() });
  assert.equal(r.decision.rule_id, 'auto_select');
  assert.equal(r.decision.profile, 'cheap');
});

test('malformed message arrays (null elements) do not crash requirement derivation', async () => {
  cfgWith();
  const { deriveRequirements } = await import('../src/gateway/router-auto.js');
  const r = deriveRequirements({ messages: [null, { role: 'user', content: 'hi' }, undefined] }, 1024);
  assert.equal(r.needs_vision, false);
  assert.ok(r.promptTokens >= 0);
});

test('digestFrom projects TEXT only — no base64 image bytes reach the memory layer', async () => {
  cfgWith();
  const { digestFrom } = await import('../src/gateway/turn.js');
  const bigB64 = 'A'.repeat(50000);
  const d = digestFrom({ messages: [null, { role: 'user', content: [{ type: 'text', text: 'analyze this diagram' }, { type: 'image_url', image_url: { url: `data:image/png;base64,${bigB64}` } }] }] });
  assert.ok(!d.user_intent.includes('AAAA'), 'base64 bytes must not appear in the digest');
  assert.match(d.user_intent, /analyze this diagram/);
  assert.match(d.user_intent, /\[image\]/);
  assert.ok(d.user_intent.length < 3000, 'intent is capped');
});

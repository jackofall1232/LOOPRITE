#!/usr/bin/env node
//
// l00prite-doctor — a read-only health check for a scaffolded project's .l00prite/ memory.
//
// Where scripts/validate-l00prite.js validates THIS repo (the protocol source), the doctor
// validates a TARGET project that was scaffolded with l00prite: it reads the project's
// .l00prite/ folder and reports whether the memory is internally consistent and safe to
// resume or arm. It is dependency-free (Node standard library only), strictly READ-ONLY
// (it never writes, and never "fixes" anything), and it exits non-zero only when a
// fail-level problem is found — so it is safe to run in CI or before arming an
// Execution Mode run.
//
//   node scripts/l00prite-doctor.js [path-to-project]   # default: current directory
//
// The check for prompt-mirror drift compares the project's OWN .l00prite/prompts against its
// OWN .claude/.codex mirrors (self-parity) — never against a hash of l00prite's canonical
// prompts, so it stays correct across legitimate protocol upgrades.

'use strict';

const fs = require('fs');
const path = require('path');

const targetArg = process.argv[2] || '.';
const root = path.resolve(targetArg);
const lp = path.join(root, '.l00prite');

const findings = [];
function ok(msg) { findings.push({ level: 'ok', msg }); }
function warn(msg, fix) { findings.push({ level: 'warn', msg, fix }); }
function fail(msg, fix) { findings.push({ level: 'fail', msg, fix }); }

function existsL(rel) { return fs.existsSync(path.join(root, rel)); }
function readL(rel) { return fs.readFileSync(path.join(root, rel), 'utf8'); }
function readJSON(rel) {
  try {
    const data = JSON.parse(readL(rel));
    if (data === null || typeof data !== 'object' || Array.isArray(data)) {
      return { ok: false, err: 'parsed value is not a JSON object' };
    }
    return { ok: true, data };
  } catch (e) { return { ok: false, err: e.message }; }
}
function isParsableDate(s) { return typeof s === 'string' && !Number.isNaN(Date.parse(s)); }

const PROMPT_NAMES = ['resume-loop', 'heartbeat', 'event-loop', 'respond-to-review', 'handoff-summary', 'execute-loop'];
const RUN_BOUNDARY_IDS = [
  'definition_of_done_met', 'iteration_limit_reached', 'human_review_gate',
  'destructive_operation_required', 'ambiguous_requirements', 'unfixable_failing_tests',
  'missing_secrets_or_credentials', 'lock_lease_conflict', 'stop_signal'
];
const REQUIRED_MEMORY = [
  'blueprint.md', 'ledger.md', 'memory.md', 'constraints.md',
  'failures.md', 'todos.md', 'heartbeat.json', 'state.json', 'lock.json', 'LOCKING.md'
];

// ---------------------------------------------------------------------------
// 0. Is this a l00prite project at all?
// ---------------------------------------------------------------------------
if (!fs.existsSync(lp)) {
  console.error(`l00prite-doctor: no .l00prite/ folder found at ${root}`);
  console.error('This does not look like a l00prite-scaffolded project. Run build-loop first,');
  console.error('or pass the project root as the first argument.');
  process.exit(2);
}

// ---------------------------------------------------------------------------
// 1. Required memory files exist and are non-empty (fail on missing/empty).
// ---------------------------------------------------------------------------
for (const name of REQUIRED_MEMORY) {
  const rel = `.l00prite/${name}`;
  if (!existsL(rel)) {
    fail(`missing required memory file: ${rel}`, `restore ${rel} from templates/l00prite/${name}`);
  } else if (readL(rel).trim().length === 0) {
    fail(`empty memory file: ${rel}`, `re-scaffold or fill ${rel}`);
  } else {
    ok(`memory file present: ${name}`);
  }
}
for (const dir of ['prompts', 'events', 'reviews', 'sessions']) {
  if (existsL(`.l00prite/${dir}`)) ok(`memory folder present: ${dir}/`);
  else warn(`missing memory folder: .l00prite/${dir}/`, `create .l00prite/${dir}/ (copy from templates/l00prite/${dir}/)`);
}

// ---------------------------------------------------------------------------
// 2. Unfilled template placeholders (warn — a scaffold that was never customized).
// ---------------------------------------------------------------------------
for (const rel of ['.l00prite/blueprint.md', '.l00prite/memory.md']) {
  if (existsL(rel)) {
    const txt = readL(rel);
    if (txt.includes('{{') || /replace-with-/i.test(txt)) {
      warn(`${rel} still contains template placeholders`, `fill in ${rel} before arming a run`);
    }
  }
}

// ---------------------------------------------------------------------------
// 3. JSON files parse (fail on parse error). heartbeat + state + lock + events.
// ---------------------------------------------------------------------------
const hbR = existsL('.l00prite/heartbeat.json') ? readJSON('.l00prite/heartbeat.json') : { ok: false, err: 'absent' };
const stR = existsL('.l00prite/state.json') ? readJSON('.l00prite/state.json') : { ok: false, err: 'absent' };
const lkR = existsL('.l00prite/lock.json') ? readJSON('.l00prite/lock.json') : { ok: false, err: 'absent' };
for (const [label, r] of [['heartbeat.json', hbR], ['state.json', stR], ['lock.json', lkR]]) {
  if (r.ok) ok(`${label} is a valid JSON object`);
  else fail(`${label} is not a valid JSON object: ${r.err}`, `${label} must be a JSON object with the expected fields`);
}
const hb = hbR.ok ? hbR.data : null;
const st = stR.ok ? stR.data : null;
const lock = lkR.ok ? lkR.data : null;

// Event JSON files parse.
const pendingDir = path.join(lp, 'events', 'pending');
let pendingEventFiles = [];
let pendingDirReadable = true;
if (fs.existsSync(pendingDir)) {
  try {
    if (fs.statSync(pendingDir).isDirectory()) {
      pendingEventFiles = fs.readdirSync(pendingDir).filter((f) => f.endsWith('.json'));
      for (const f of pendingEventFiles) {
        try { JSON.parse(fs.readFileSync(path.join(pendingDir, f), 'utf8')); }
        catch (e) { fail(`events/pending/${f} does not parse: ${e.message}`, `fix the JSON in that event file`); }
      }
    } else {
      pendingDirReadable = false;
      fail('.l00prite/events/pending exists but is not a directory', 'events/pending/ must be a directory holding pending event JSON files');
    }
  } catch (e) {
    pendingDirReadable = false;
    fail(`could not read .l00prite/events/pending: ${e.message}`, 'check permissions or recreate the directory');
  }
}

// ---------------------------------------------------------------------------
// 4. Execution block + arming consistency (the core safety check).
//    Mirrors validate-l00prite.js: enabled/active is legal ONLY with a matching active,
//    unexpired execute-loop lock; otherwise a crashed run left arming state committed.
// ---------------------------------------------------------------------------
const exec = hb && hb.execution && typeof hb.execution === 'object' ? hb.execution : null;
if (hb && !exec) {
  warn('heartbeat.json has no execution block (v1 schema)', 'execute-loop migrates it under lock; a missing block means Execution Mode is disabled');
} else if (exec) {
  ok('heartbeat.json carries the execution block');
  const boundaries = Array.isArray(exec.run_boundaries) ? exec.run_boundaries : [];
  const missing = RUN_BOUNDARY_IDS.filter((id) => !boundaries.includes(id));
  if (missing.length) warn(`execution.run_boundaries missing: ${missing.join(', ')}`, 'restore the full run_boundaries list from templates/l00prite/heartbeat.json');
  else ok('execution.run_boundaries lists all nine boundaries');
  if (!(typeof exec.max_iterations === 'number' && exec.max_iterations > 0)) {
    warn('execution.max_iterations is not a positive number', 'set a bounded max_iterations — never leave a run unbounded');
  }
}

let armedMidRun = false; // an active, unexpired lock that legitimately backs a running execution
if (hb && st) {
  const enabled = exec ? exec.enabled === true : false;
  const active = st.execution_active === true;

  // Is there a matching active, unexpired execute-loop lock right now?
  let lockActiveExecute = false;
  if (lock) {
    const notExpired = isParsableDate(lock.expires_at) && Date.parse(lock.expires_at) > Date.now();
    lockActiveExecute = lock.status === 'active' && notExpired &&
      typeof lock.purpose === 'string' && lock.purpose.includes('execute-loop');
  }

  if (enabled !== active) {
    fail(`arming mismatch: heartbeat execution.enabled=${enabled} but state.execution_active=${active}`,
      'set both to false and record the reconciliation in ledger.md, or re-run execute-loop pre-flight');
  } else if (enabled || active) {
    if (lockActiveExecute) {
      armedMidRun = true;
      ok('Execution Mode is armed under a matching active execute-loop lock (mid-run)');
    } else {
      fail('Execution Mode is armed but no active, unexpired execute-loop lock backs it — a crashed run left arming state committed',
        'execute-loop pre-flight stale-run recovery will reset this; or set execution.enabled=false / execution_active=false and log it');
    }
  } else {
    ok('Execution Mode ships disarmed (enabled=false, execution_active=false)');
  }

  // preflight_confirmed left true while disarmed and unlocked is a stale audit artifact.
  if (exec && exec.preflight_confirmed === true && !enabled && !lockActiveExecute) {
    warn('execution.preflight_confirmed is true while Execution Mode is disarmed', 'a persisted preflight flag never authorizes a run; it is safe but stale — the next pre-flight overwrites it');
  }

  // Surface a hard stop: a blocked project needs a human before it resumes.
  if (st.blocked === true) {
    const reason = st.blocker_reason ? `: ${st.blocker_reason}` : ' (no blocker_reason recorded)';
    warn(`project is blocked${reason}`, 'resolve the blocker and set state.blocked=false before resuming');
    // blocked must win over should_continue.
    if (hb.should_continue === true) {
      warn('state.blocked is true while heartbeat.should_continue is true', 'blocked wins by protocol — ensure the loop stops, and clear one of the two signals');
    }
  }
}

// ---------------------------------------------------------------------------
// 5. Lock sanity (warn only — a stale active lock is reclaimable, not fatal).
// ---------------------------------------------------------------------------
if (lock) {
  const required = ['schema_version', 'lock_id', 'acquired_at', 'expires_at', 'ttl_seconds', 'status', 'protected_paths'];
  const missing = required.filter((f) => !Object.prototype.hasOwnProperty.call(lock, f));
  if (missing.length) warn(`lock.json missing fields: ${missing.join(', ')}`, 'restore the lock.json shape from templates/l00prite/lock.json');
  // A malformed timestamp makes the expiry logic silently wrong — surface it rather than
  // letting Date.parse return NaN and skip the expiry check.
  for (const field of ['acquired_at', 'expires_at']) {
    if (typeof lock[field] === 'string' && Number.isNaN(Date.parse(lock[field]))) {
      fail(`lock.json ${field} is not a parseable ISO 8601 date: ${JSON.stringify(lock[field])}`,
        `set ${field} to a valid ISO 8601 timestamp (e.g. 2026-07-04T09:00:00Z)`);
    }
  }
  const active = lock.status === 'active' && isParsableDate(lock.expires_at);
  if (active && Date.parse(lock.expires_at) <= Date.now()) {
    warn('lock.json is "active" but expired', 'the next agent may reclaim it and log the reclamation per LOCKING.md');
  } else if (active && Date.parse(lock.expires_at) > Date.now() && !armedMidRun) {
    // A live lock held by someone else: protected memory is in use — do not resume or arm.
    const owner = lock.owner_agent || lock.owner_session || 'unknown';
    warn(`an active, unexpired lock is held (owner=${owner}, purpose=${lock.purpose || 'unspecified'}, expires=${lock.expires_at})`,
      'protected memory is in use — do not resume or arm until the lock is released or expires, per LOCKING.md');
  }
}

// ---------------------------------------------------------------------------
// 6. Event bookkeeping: pending file count vs state.pending_event_count (State Rot).
// ---------------------------------------------------------------------------
if (st && pendingDirReadable && Object.prototype.hasOwnProperty.call(st, 'pending_event_count')) {
  if (st.pending_event_count !== pendingEventFiles.length) {
    warn(`state.pending_event_count=${st.pending_event_count} but events/pending/ holds ${pendingEventFiles.length} event file(s)`,
      'reconcile the counter with the files — a common State Rot signal');
  } else {
    ok(`pending event count matches events/pending/ (${pendingEventFiles.length})`);
  }
}

// ---------------------------------------------------------------------------
// 7. Ledger verification evidence (Verifier Theater). Heuristic, warn-level.
// ---------------------------------------------------------------------------
if (existsL('.l00prite/ledger.md')) {
  const ledger = readL('.l00prite/ledger.md');
  // Only inspect the actual run history, not the entry-template preamble (which always
  // contains the words command/exit_code/evidence_path and would mask a real evidence-free run).
  const runsIdx = ledger.search(/^##\s+Runs\s*$/im);
  if (runsIdx >= 0) {
    const runs = ledger.slice(runsIdx + '## Runs'.length);
    const substantive = runs.replace(/\s+/g, ' ').trim().length > 200;
    if (substantive) {
      // Require a concrete verification signal (an exit code or an evidence path), not the
      // mere presence of a "Tests run / Verification" field label, which every entry carries.
      const hasEvidence = /(exit_code|evidence_path)/i.test(runs);
      if (!hasEvidence) {
        warn('ledger.md has run entries but no visible verification evidence (command/exit_code/tests)', 'record command + exit_code + timestamp per ledger entry — otherwise "verified" is unaudited');
      } else {
        ok('ledger.md run entries carry verification evidence');
      }
    }
  }
}

// ---------------------------------------------------------------------------
// 8. Seeded failure catalog + Autonomous-Edit Denylist present (loop wisdom + path safety).
// ---------------------------------------------------------------------------
if (existsL('.l00prite/failures.md')) {
  if (/inherited loop failure modes/i.test(readL('.l00prite/failures.md'))) ok('failures.md carries the inherited failure-mode catalog');
  else warn('failures.md is missing the inherited failure-mode catalog', 'seed it from templates/l00prite/failures.md so a fresh agent reads known failure modes');
}
if (existsL('.l00prite/constraints.md')) {
  const c = readL('.l00prite/constraints.md');
  const secIdx = c.search(/autonomous-edit denylist/i);
  if (secIdx < 0) {
    warn('constraints.md has no Autonomous-Edit Denylist', 'add a machine-readable protected-paths block so Execution Mode cannot edit secrets/auth/migrations without review');
  } else {
    // The heading alone is not enough: execute-loop enforces the fenced glob block, so require
    // the block to exist and still carry the critical shipped protections.
    const fence = c.slice(secIdx).match(/```[a-z]*\n([\s\S]*?)```/i);
    const block = fence ? fence[1] : '';
    const critical = [{ re: /(^|\/|\*)\.env/im, name: '.env' }, { re: /secret|credential/i, name: 'secrets/credentials' }];
    const missing = critical.filter((p) => !p.re.test(block)).map((p) => p.name);
    if (!block) {
      warn('Autonomous-Edit Denylist heading present but no fenced glob block found', 'restore the machine-readable ``` glob block under the heading');
    } else if (missing.length) {
      warn(`Autonomous-Edit Denylist is missing critical protections: ${missing.join(', ')}`, 'restore the shipped secret/credential/.env patterns — execute-loop relies on this list before every edit');
    } else {
      ok('constraints.md defines an Autonomous-Edit Denylist with the critical protections');
    }
  }
}

// ---------------------------------------------------------------------------
// 9. No-progress stall telemetry (thrash circuit-breaker observability).
// ---------------------------------------------------------------------------
if (exec) {
  const isp = typeof exec.iterations_since_progress === 'number' ? exec.iterations_since_progress : 0;
  const threshold = typeof exec.no_progress_threshold === 'number' && exec.no_progress_threshold > 0 ? exec.no_progress_threshold : 3;
  if (isp >= threshold) {
    const active = st && st.execution_active === true;
    const msg = `execution.iterations_since_progress=${isp} has reached the no_progress_threshold (${threshold}) — the loop may be thrashing`;
    if (active) fail(msg, 'stop and escalate through human_review_gate; diagnose why no unit is closing before resuming');
    else warn(msg, 'leftover stall telemetry from a prior run; investigate before re-arming');
  }
}

// ---------------------------------------------------------------------------
// 10. Prompt-mirror self-parity (drift). Compare the project's OWN copies.
// ---------------------------------------------------------------------------
const mirrorDirs = ['.claude/prompts', '.codex/prompts'].filter((d) => existsL(d));
if (existsL('.l00prite/prompts')) {
  // The six loop prompts must all be present in .l00prite/prompts — a missing one is not
  // "no drift", it is a project missing a loop procedure build-loop promised to scaffold.
  let baseMissing = 0;
  for (const name of PROMPT_NAMES) {
    if (!existsL(`.l00prite/prompts/${name}.md`)) {
      baseMissing++;
      fail(`missing loop prompt: .l00prite/prompts/${name}.md`, 're-scaffold the canonical loop prompts');
    }
  }
  let drift = 0, compared = 0;
  for (const dir of mirrorDirs) {
    // Once a vendor prompt directory exists, it must carry every loop prompt, byte-identical.
    for (const name of PROMPT_NAMES) {
      const base = `.l00prite/prompts/${name}.md`;
      const mirror = `${dir}/${name}.md`;
      if (!existsL(base)) continue; // already failed above
      if (!existsL(mirror)) {
        drift++;
        fail(`missing prompt mirror: ${mirror} (present in .l00prite/prompts/)`, `copy ${name}.md into ${dir} so vendor agents get the loop prompts`);
        continue;
      }
      compared++;
      if (readL(mirror) !== readL(base)) {
        drift++;
        fail(`prompt drift: ${mirror} differs from .l00prite/prompts/${name}.md`, 're-copy the canonical prompt so mirrors are byte-identical');
      }
    }
  }
  if (mirrorDirs.length && compared && !drift) ok(`prompt mirrors are byte-identical to .l00prite/prompts (${compared} compared)`);
  else if (!mirrorDirs.length && !baseMissing) ok('prompt self-parity skipped (no .claude/.codex prompt mirrors in this project)');
}

// ---------------------------------------------------------------------------
// Report
// ---------------------------------------------------------------------------
const counts = { ok: 0, warn: 0, fail: 0 };
for (const f of findings) counts[f.level]++;

const LABEL = { ok: 'OK  ', warn: 'WARN', fail: 'FAIL' };
console.log(`l00prite-doctor — ${lp}\n`);
for (const f of findings) {
  console.log(`${LABEL[f.level]}  ${f.msg}`);
  if (f.fix && f.level !== 'ok') console.log(`      ↳ ${f.fix}`);
}
console.log(`\n${counts.ok} ok · ${counts.warn} warn · ${counts.fail} fail`);

let verdict;
if (counts.fail > 0) verdict = `UNHEALTHY — ${counts.fail} blocking issue(s); resolve before resuming or arming a run.`;
else if (counts.warn > 0) verdict = `OK WITH WARNINGS — ${counts.warn} advisory item(s); review before an unattended run.`;
else verdict = 'HEALTHY — .l00prite/ memory is consistent and Execution Mode ships disarmed.';
console.log(verdict);

process.exit(counts.fail > 0 ? 1 : 0);

// kgai-mod: the end-of-turn capture check of the kgai plugin, run on the side.
//
// The kgai plugin's Stop hook (hooks/auto-capture-stop.sh) continues a turn that edited
// code with an instruction to record any structural decision, so the model always answers
// it with a visible line, most often "nothing to record". This mod asks the same question
// as a fork of the session ($.model.fork: no tools, no transcript row) once the turn is
// over, ingests what it proposes, and removes that one instruction from the classic Stop
// result. The kgai plugin is still required: it installs the engine and ships the skills.
//
// It also draws kgai's own tool calls (kg search, kg ingest, the kgai skills) as one dim
// line each instead of the command and its JSON; see rows.ts.
import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { KgaiModPending } from '../types'
import { kgRow } from './rows'

const pending = atom({ plugin: 'kgai-mod', key: 'pending' } as const, null)
const hasToastedFailure = atom({ plugin: 'kgai-mod', key: 'hasToastedFailure' } as const, false)

// The kgai skill that records a decision; any `kg ingest` the model runs counts the same.
const RECORD_SKILL = /(^|:)kg-decision$/
const INGEST_COMMAND = /\bkg\s+ingest\b/

// How auto-capture-stop.sh starts its nudge. tests/hooks-contract.sh keeps the two equal.
export const CLASSIC_NUDGE_PREFIX = 'Before you stop: this turn edited code.'

const STATUS_MS = 6000
const FAILURE_TOAST = 'kgai: capture check failed, see --debug'

export const CAPTURE_PROMPT = `kgai end-of-turn check. This is a side question: your answer is not shown to the user, and you cannot use tools.

The turn that just ended edited code. Decide whether it made a STRUCTURAL decision about the codebase that belongs in the kgai knowledge graph, applying the knowledge-graph skill's rules strictly:

- DO record: splitting, merging or moving a module or feature; changing a dependency or an ownership boundary; deciding how something is exposed or rendered; deprecating or replacing a prior structural decision; renaming a domain element (its canonical name changes).
- DON'T record: code-level renames of files, functions or variables; behavior-preserving refactors; formatting; bug fixes that restore intended behavior; pure implementation details; analyses, reports or recommendations nobody acted on. When in doubt, don't.

Answer with exactly one of these and nothing else (no prose, no code fence):
1. The single word NONE.
2. The JSON payload for \`kg ingest\`: {"decision": {...}} or {"decisions": [{...}, ...]}. Each decision has "title" (one line), "rationale" (2-3 sentences on why) and "mutations", and attaches to at least one element. Mutation ops: {"op": "upsert_element", "kind": "feature", "name": "Invoice", "props": {...}} (props optional), {"op": "add_link" or "retire_link", "from": "kind:name", "link": "PART_OF", "to": "kind:name"}, {"op": "set_prop", "element": "kind:name", "key": "...", "value": "..."}. Reuse element names already used in this session exactly. Leave out "author".`

type Capture = 'auto' | 'confirm' | 'off'
type Proposal = { kind: 'none' } | { kind: 'payload'; payload: string; title: string } | { kind: 'invalid'; why: string }

// Reads the fork's answer: NONE, or a `kg ingest` payload (a stray code fence tolerated).
export const parseAnswer = (text: string): Proposal => {
  const body = text
    .trim()
    .replace(/^```[a-z]*\s*/i, '')
    .replace(/\s*```$/, '')
    .trim()

  if (/^NONE\b/i.test(body)) {
    return { kind: 'none' }
  }

  let parsed: unknown
  try {
    parsed = JSON.parse(body)
  } catch {
    return { kind: 'invalid', why: 'answer is neither NONE nor JSON' }
  }

  const titleOf = (d: unknown) =>
    typeof d === 'object' && d !== null && typeof (d as { title?: unknown }).title === 'string'
      ? (d as { title: string }).title
      : undefined
  const root = (typeof parsed === 'object' && parsed !== null ? parsed : {}) as { decision?: unknown; decisions?: unknown }
  const titles = Array.isArray(root.decisions) ? root.decisions.map(titleOf) : [titleOf(root.decision)]

  if (titles.length === 0 || titles.some(t => t === undefined)) {
    return { kind: 'invalid', why: 'payload has no decision with a title' }
  }

  const title = titles.length === 1 ? `"${titles[0]}"` : `${titles.length} decisions`

  return { kind: 'payload', payload: JSON.stringify(parsed), title }
}

let statusTimer: { cancel: () => void } | undefined

// Tool rows drawn as one line: their result block under them is left out too. A ToolResult
// carries no input to tell a kg call by, and its ToolUse row is always drawn first.
const compacted = new Set<string>()
const COMPACTED_MAX = 500

function compact(id: string) {
  compacted.add(id)
  if (compacted.size > COMPACTED_MAX) {
    const oldest = compacted.values().next().value
    if (oldest !== undefined) {
      compacted.delete(oldest)
    }
  }
}

function debug($: EngineInterface, text: string) {
  return $.ui.log(`kgai-mod: ${text}`, { to: 'debug' })
}

async function fail($: EngineInterface, why: string) {
  debug($, `capture check failed: ${why}`)
  if (!(await read($, hasToastedFailure))) {
    await update($, hasToastedFailure, () => true)
    $.ui.toast(FAILURE_TOAST)
  }
}

// The engine as the bash hooks find it: $KGAI_HOME/bin/kg (default ~/.kgai), else PATH.
async function kg($: EngineInterface) {
  const home = (await $.env.get('KGAI_HOME')) ?? `${(await $.env.get('HOME')) ?? ''}/.kgai`
  const libs = `${home}/lib`
  const own = await $.fs.stat(`${home}/bin/kg`).catch(() => undefined)
  const bin = own?.kind === 'file' ? `${home}/bin/kg` : 'kg'
  const ld = await $.env.get('LD_LIBRARY_PATH')
  const dyld = await $.env.get('DYLD_LIBRARY_PATH')
  const env = {
    LD_LIBRARY_PATH: ld ? `${libs}:${ld}` : libs,
    DYLD_LIBRARY_PATH: dyld ? `${libs}:${dyld}` : libs,
  }

  return (args: string[], stdin: string) => $.process.run([bin, ...args], { stdin, env, timeoutMs: 20000 })
}

// Dry run first (resolves names, refuses a bad payload without writing), then for real.
async function ingest($: EngineInterface, payload: string, dryRunOnly = false) {
  const run = await kg($)
  for (const args of dryRunOnly ? [['ingest', '--dry-run']] : [['ingest', '--dry-run'], ['ingest']]) {
    const out = await run(args, payload)
    if (out.exitCode !== 0) {
      return `kg ${args.join(' ')} exited ${out.exitCode}: ${(out.stdout || out.stderr).trim().slice(0, 300)}`
    }
  }
  return undefined
}

async function announce($: EngineInterface, title: string) {
  const line = `kgai: recorded ${title}`
  debug($, line)
  // A session nothing draws on (claude -p, an SDK or stream-json host) gets the line as
  // ui_log; a drawn one keeps its transcript clean and shows a passing status line.
  if ((await $.session.surfaces()).length === 0) {
    $.ui.log(line)
    return
  }
  $.ui.status(line)
  statusTimer?.cancel()
  statusTimer = $.clock.after(STATUS_MS, () => $.ui.status(undefined))
}

async function check($: EngineInterface, capture: Capture) {
  if (await $.env.get('KGAI_DISABLE_HOOKS')) {
    return
  }
  const reply = await $.model.fork({ prompt: CAPTURE_PROMPT })
  if (!reply.isAnswered) {
    return fail($, `fork not answered (${reply.reason})`)
  }

  const proposal = parseAnswer(reply.text)
  if (proposal.kind === 'none') {
    return debug($, 'no structural decision this turn')
  }
  if (proposal.kind === 'invalid') {
    return fail($, proposal.why)
  }

  // confirm needs a band to draw on; this build raises AbovePrompt on the terminal and
  // the desktop alone, so anywhere else (headless included) it records as auto does.
  const surfaces = await $.session.surfaces()
  const hasBand = surfaces.some(s => s === 'terminal' || s === 'desktop')
  if (capture === 'confirm' && hasBand) {
    const invalid = await ingest($, proposal.payload, true)
    if (invalid !== undefined) {
      return fail($, invalid)
    }
    const proposed: KgaiModPending = { title: proposal.title, payload: proposal.payload }
    return update($, pending, () => proposed)
  }

  const error = await ingest($, proposal.payload)
  if (error !== undefined) {
    return fail($, error)
  }
  return announce($, proposal.title)
}

export const register: Register = (on, options) => {
  const capture: Capture = options.capture === 'confirm' || options.capture === 'off' ? options.capture : 'auto'

  // What this turn did, counted from the tool calls themselves. Not `kg turn take`: the
  // kgai plugin's Stop hook reads and clears those marks before this mod's turn.complete.
  let edited = 0
  let recorded = false
  let forkedTurnId: string | undefined

  if (options.compact !== false) {
    on('ui.render', { component: 'ToolUse' }, ($, e, next) => {
      const row = e.props.isErrored || e.props.isInterrupted ? undefined : kgRow(e.props.tool, e.props.input, e.props.output)
      if (row === undefined) {
        compacted.delete(e.props.tool_use_id)
        return next(e)
      }
      compact(e.props.tool_use_id)
      const { Text } = $.ui.resolve(e)
      return <Text dimColor>{e.props.isRunning ? row.running : row.done}</Text>
    })

    on('ui.render', { component: 'ToolResult' }, ($, e, next) => {
      if (e.props.isErrored || !compacted.has(e.props.tool_use_id)) {
        return next(e)
      }
      const { Box } = $.ui.resolve(e)
      return <Box />
    })
  }

  if (capture === 'off') {
    return
  }

  on('turn.start', ($, e, next) => {
    edited = 0
    recorded = false
    return next(e)
  })

  on('prompt.submit', async ($, e, next) => {
    // The band belongs to the turn that proposed it.
    if ((await read($, pending)) !== null) {
      await update($, pending, () => null)
    }
    return next(e)
  })

  on('tool.call', { tool: /^(Edit|Write|MultiEdit|NotebookEdit)$/ }, async ($, e, next) => {
    const ran = await next(e)
    if (ran.deny === undefined && ran.isError !== true) {
      edited += 1
    }
    return ran
  })

  on('tool.call', { tool: 'Bash' }, async ($, e, next) => {
    const ran = await next(e)
    if (ran.deny === undefined && ran.isError !== true && INGEST_COMMAND.test(e.command) && !e.command.includes('--dry-run')) {
      recorded = true
    }
    return ran
  })

  on('tool.call', { tool: 'Skill' }, async ($, e, next) => {
    const ran = await next(e)
    if (ran.deny === undefined && RECORD_SKILL.test(e.skill)) {
      recorded = true
    }
    return ran
  })

  // Both kgai Stop commands still run beneath (auto-sync.sh spawns its sync); only the
  // capture instruction is taken out of the folded result, so the turn is not continued.
  on('classic.Stop', async ($, e, next) => {
    const r = await next(e)
    const isNudge = (text: string) => text.startsWith(CLASSIC_NUDGE_PREFIX)
    const context = r.additionalContext?.filter(text => !isNudge(text))
    const isBlockNudge = r.block !== undefined && isNudge(r.block)
    if (context?.length === r.additionalContext?.length && !isBlockNudge) {
      return r
    }
    debug($, 'classic capture nudge suppressed')
    const { additionalContext: _context, block: _block, ...rest } = r
    return {
      ...rest,
      ...(context && context.length > 0 ? { additionalContext: context } : {}),
      ...(r.block !== undefined && !isBlockNudge ? { block: r.block } : {}),
    }
  })

  on('turn.complete', async ($, e, next) => {
    const r = await next(e)
    const isDue = e.agentId === undefined && e.reason === 'answer' && edited > 0 && !recorded && forkedTurnId !== e.turnId
    if (!isDue) {
      return r
    }
    forkedTurnId = e.turnId
    edited = 0
    const run = () => check($, capture).catch((err: unknown) => fail($, err instanceof Error ? err.message : String(err)))
    // A session nothing draws on (claude -p) ends with its turn and would drop a check left
    // running, and nobody waits at a prompt there: check before the turn is over.
    if ((await $.session.surfaces()).length === 0) {
      await run()
      return r
    }
    // Otherwise off the turn's own dispatch, so the turn ends now and the check runs beside it.
    $.clock.after(0, () => {
      void run()
    })
    return r
  })

  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    const proposed = await read($, pending)
    if (proposed === null || e.props.hasSurvey) {
      return next(e)
    }

    const { Box, Button, Text } = $.ui.resolve(e)
    const record = async () => {
      await update($, pending, () => null)
      const error = await ingest($, proposed.payload)
      return error === undefined ? announce($, proposed.title) : fail($, error)
    }
    const skip = () => update($, pending, () => null)

    return (
      <Box flexDirection="row" gap={1}>
        <Text wrap="truncate-end">
          kgai: record {proposed.title}?
        </Text>
        <Button key="record" label="Record" hotkey="r" variant="primary" onPress={record} />
        <Button key="skip" label="Skip" hotkey="s" onPress={skip} />
      </Box>
    )
  })
}

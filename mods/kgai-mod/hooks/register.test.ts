import { describe, expect, mock, test } from 'claude-code/testing'
import type { ClassicResultOf, ModelForkResult, On, RenderSurface } from 'claude-code'
import type { Engine } from 'claude-code/testing'

import { CLASSIC_NUDGE_PREFIX, parseAnswer } from './register'

const KG = '/home/dev/.kgai/bin/kg'
const PAYLOAD = '{"decision":{"title":"Invoice renders standalone","rationale":"Own domain.","mutations":[{"op":"upsert_element","kind":"feature","name":"Invoice"}]}}'
const USAGE = { input_tokens: 10, output_tokens: 5, cache_read_input_tokens: 9000, cache_creation_input_tokens: 0 }
const NUDGE = `${CLASSIC_NUDGE_PREFIX} If it involved a STRUCTURAL/architectural decision ... or stop.`

type World = {
  fork?: ModelForkResult
  ingestExit?: number
  surfaces?: readonly RenderSurface[]
  stop?: ClassicResultOf['classic.Stop']
}

// Everything beneath the mod: the engine's answers, recorded so a test can look at them.
const world = (on: On, { fork = { isAnswered: true, text: PAYLOAD, usage: USAGE }, ingestExit = 0, surfaces = ['terminal'], stop = { additionalContext: [NUDGE] } }: World = {}) => {
  const seen = {
    forks: 0,
    runs: [] as { argv: readonly string[]; stdin: string | undefined }[],
    status: [] as (string | undefined)[],
    toasts: [] as string[],
    logs: [] as { text: string; to: string }[],
    appends: 0,
    syncs: 0,
  }
  const clock = mock.clock(on, { now: 1_000 })
  mock.env(on, { HOME: '/home/dev' })

  on('fs.stat', ($, e) => {
    if (e.path !== KG) {
      throw new Error('ENOENT')
    }
    return { value: { kind: 'file', size: 1, mtimeMs: 0, isLink: false } }
  })
  on('process.run', ($, e) => {
    seen.runs.push({ argv: e.argv, stdin: e.init?.stdin })
    const exitCode = e.argv.includes('--dry-run') ? 0 : ingestExit
    const stdout = exitCode === 0 ? '{"ok":true}' : '{"ok":false,"error":"store locked"}'
    return { value: { exitCode, stdout, stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
  })
  on('model.fork', () => {
    seen.forks += 1
    return { value: fork }
  })
  on('session.surfaces', () => ({ value: surfaces }))
  on('ui.status', ($, e) => {
    seen.status.push(e.text)
    return { value: undefined }
  })
  on('ui.toast', ($, e) => {
    seen.toasts.push(e.text)
    return { value: undefined }
  })
  on('ui.log', ($, e) => {
    seen.logs.push({ text: e.text, to: e.to })
    return { value: undefined }
  })
  on('session.append', ($, e) => {
    seen.appends += 1
    return { message: e.message, uuid: 'row' }
  })
  on('turn.start', ($, e) => ({ turnId: e.turnId }))
  on('turn.complete', ($, e) => ({ text: e.answer }))
  on('prompt.submit', ($, e) => ({ text: e.text }))
  // What the engine draws in the band when no plugin does: here, an empty Box.
  on('ui.render', () => ({ type: 'Box', props: {}, children: [] }))
  on('tool.call', () => ({ result: undefined as never, text: 'done' }))
  // The kgai plugin's two Stop commands, folded as the engine folds them: the capture
  // nudge (auto-capture-stop.sh) and the sync spawn (auto-sync.sh), which prints nothing.
  on('classic.Stop', () => {
    seen.syncs += 1
    return stop
  })

  const ingests = () => seen.runs.filter(r => !r.argv.includes('--dry-run'))
  const transcriptLogs = () => seen.logs.filter(l => l.to !== 'debug')
  const debugLogs = () => seen.logs.filter(l => l.to === 'debug')

  return { seen, clock, ingests, transcriptLogs, debugLogs }
}

let turns = 0

// One main-loop turn: start, the given tool calls, the end, then whatever the end left running.
const turn = async ($: Engine, w: ReturnType<typeof world>, calls: Parameters<Engine['tool']['call']>[0][] = []) => {
  const turnId = `t${++turns}`
  await $.turn.start({ text: 'do it', turnId })
  for (const call of calls) {
    await $.tool.call(call)
  }
  const done = await $.turn.complete({ answer: 'Done.', durationMs: 900, isAborted: false, turnId, reason: 'answer' })
  await w.clock.settle()
  return done
}

const BAND = {
  component: 'AbovePrompt',
  props: { hasSurvey: false, isWorking: false, maxRows: 10, bodyColumns: 100, scroll: { offset: 0, bodyRows: 10 }, view: {} },
} as const

const EDIT = { tool: 'Edit', file_path: '/repo/src/a.go', old_string: 'a', new_string: 'b' } as const

describe('auto capture', () => {
  test('an edit and a payload from the fork: ingested, status shown, transcript untouched', async ($, on) => {
    const w = world(on)
    const done = await turn($, w, [EDIT])

    expect(w.seen.forks).toBe(1)
    expect(w.seen.runs.map(r => r.argv)).toEqual([
      [KG, 'ingest', '--dry-run'],
      [KG, 'ingest'],
    ])
    expect(w.ingests()[0]?.stdin).toBe(PAYLOAD)
    expect(w.seen.status).toEqual(['kgai: recorded "Invoice renders standalone"'])
    expect(done.text).toBe('Done.')
    expect(w.seen.appends).toBe(0)
    expect(w.transcriptLogs()).toHaveLength(0)
    expect(w.seen.toasts).toHaveLength(0)

    await w.clock.advance(6000)
    expect(w.seen.status).toEqual(['kgai: recorded "Invoice renders standalone"', undefined])
  })

  test('the fork answers NONE: no ingest, no status, no toast', async ($, on) => {
    const w = world(on, { fork: { isAnswered: true, text: 'NONE', usage: USAGE } })
    await turn($, w, [EDIT])

    expect(w.seen.forks).toBe(1)
    expect(w.seen.runs).toHaveLength(0)
    expect(w.seen.status).toHaveLength(0)
    expect(w.seen.toasts).toHaveLength(0)
    expect(w.transcriptLogs()).toHaveLength(0)
  })

  test('a turn without edits asks nothing', async ($, on) => {
    const w = world(on)
    await turn($, w, [{ tool: 'Bash', command: 'go test ./...' }])

    expect(w.seen.forks).toBe(0)
    expect(w.seen.runs).toHaveLength(0)
  })

  test('a turn that already ran kg ingest asks nothing', async ($, on) => {
    const w = world(on)
    await turn($, w, [EDIT, { tool: 'Bash', command: "kg ingest <<'JSON'\n{}\nJSON" }])

    expect(w.seen.forks).toBe(0)
  })

  test('a turn that ran the kg-decision skill asks nothing', async ($, on) => {
    const w = world(on)
    await turn($, w, [EDIT, { tool: 'Skill', skill: 'kgai:kg-decision' }])

    expect(w.seen.forks).toBe(0)
  })

  test('a kg ingest dry run alone is not a recording', async ($, on) => {
    const w = world(on)
    await turn($, w, [EDIT, { tool: 'Bash', command: 'kg ingest --dry-run < d.json' }])

    expect(w.seen.forks).toBe(1)
  })

  test('the counters start over each turn', async ($, on) => {
    const w = world(on)
    await turn($, w, [EDIT])
    await turn($, w)

    expect(w.seen.forks).toBe(1)
  })

  test('an interrupted turn asks nothing', async ($, on) => {
    const w = world(on)
    await $.turn.start({ text: 'do it', turnId: 'aborted' })
    await $.tool.call(EDIT)
    await $.turn.complete({ answer: '', durationMs: 900, isAborted: true, turnId: 'aborted', reason: 'aborted' })
    await w.clock.settle()

    expect(w.seen.forks).toBe(0)
  })

  test('the fork not answered, then a failing ingest: one debug line each, one toast, nothing in the transcript', async ($, on) => {
    const w = world(on, { fork: { isAnswered: false, reason: 'api-error', status: 529, error: 'overloaded', usage: USAGE } })
    await turn($, w, [EDIT])

    expect(w.seen.runs).toHaveLength(0)
    expect(w.debugLogs()).toHaveLength(1)
    expect(w.debugLogs()[0]?.text).toContain('fork not answered (api-error)')
    expect(w.seen.toasts).toEqual(['kgai: capture check failed, see --debug'])
    expect(w.transcriptLogs()).toHaveLength(0)
    expect(w.seen.appends).toBe(0)
  })

  test('an ingest that exits non-zero: one debug line, one toast per session, nothing in the transcript', async ($, on) => {
    const w = world(on, { ingestExit: 1 })
    await turn($, w, [EDIT])
    await turn($, w, [EDIT])

    expect(w.ingests()).toHaveLength(2)
    expect(w.debugLogs()).toHaveLength(2)
    expect(w.debugLogs()[0]?.text).toContain('exited 1')
    expect(w.seen.toasts).toEqual(['kgai: capture check failed, see --debug'])
    expect(w.seen.status).toHaveLength(0)
    expect(w.transcriptLogs()).toHaveLength(0)
    expect(w.seen.appends).toBe(0)
  })

  test('headless: the check is done when the turn is, since claude -p exits with it', async ($, on) => {
    const w = world(on, { surfaces: [] })
    await $.turn.start({ text: 'do it', turnId: 'headless' })
    await $.tool.call(EDIT)
    await $.turn.complete({ answer: 'Done.', durationMs: 900, isAborted: false, turnId: 'headless', reason: 'answer' })

    expect(w.seen.forks).toBe(1)
    expect(w.ingests()).toHaveLength(1)
  })

  test('interactive: the turn ends before the check runs', async ($, on) => {
    const w = world(on)
    await $.turn.start({ text: 'do it', turnId: 'interactive' })
    await $.tool.call(EDIT)
    await $.turn.complete({ answer: 'Done.', durationMs: 900, isAborted: false, turnId: 'interactive', reason: 'answer' })

    expect(w.seen.forks).toBe(0)
    await w.clock.settle()
    expect(w.seen.forks).toBe(1)
  })

  test('headless (nothing draws): the record reaches the host as one ui_log line', async ($, on) => {
    const w = world(on, { surfaces: [] })
    await turn($, w, [EDIT])

    expect(w.ingests()).toHaveLength(1)
    expect(w.transcriptLogs().map(l => l.text)).toEqual(['kgai: recorded "Invoice renders standalone"'])
    expect(w.seen.status).toHaveLength(0)
  })
})

test('capture off: nothing happens and the classic nudge stays', { options: { capture: 'off' } }, async ($, on) => {
  const w = world(on)
  await turn($, w, [EDIT])
  const stop = await $.classic.Stop({ stop_hook_active: false })

  expect(w.seen.forks).toBe(0)
  expect(w.seen.runs).toHaveLength(0)
  expect(stop.additionalContext).toEqual([NUDGE])
})

describe('confirm', () => {
  for (const surface of ['terminal', 'desktop', 'vscode'] as const) {
    test(`a band with Record / Skip on ${surface}; Record ingests`, { options: { capture: 'confirm' } }, async ($, on) => {
      const w = world(on)
      await turn($, w, [EDIT])

      // Validated, not yet written.
      expect(w.seen.runs.map(r => r.argv)).toEqual([[KG, 'ingest', '--dry-run']])

      const ui = await $.ui.mount({ plugin: 'kgai-mod', surface, ...BAND })
      expect(await ui.find({ type: 'Text', text: 'Invoice renders standalone' })).toBeDefined()
      expect(await ui.findAll({ type: 'Button' })).toHaveLength(2)

      await ui.press({ key: 'record' })
      expect(w.ingests().map(r => r.stdin)).toEqual([PAYLOAD])
      expect(w.seen.status).toEqual(['kgai: recorded "Invoice renders standalone"'])
      expect(await ui.find({ key: 'record' })).toBeUndefined()
      await ui.unmount()
    })
  }

  test('Skip records nothing and the band is gone', { options: { capture: 'confirm' } }, async ($, on) => {
    const w = world(on)
    await turn($, w, [EDIT])
    const ui = await $.ui.mount({ plugin: 'kgai-mod', surface: 'terminal', ...BAND })

    await ui.press({ key: 'skip' })
    expect(w.ingests()).toHaveLength(0)
    expect(await ui.find({ key: 'record' })).toBeUndefined()
    await ui.unmount()
  })

  test('the next prompt takes the band down', { options: { capture: 'confirm' } }, async ($, on) => {
    const w = world(on)
    await turn($, w, [EDIT])
    const ui = await $.ui.mount({ plugin: 'kgai-mod', surface: 'terminal', ...BAND })
    expect(await ui.find({ key: 'record' })).toBeDefined()

    await $.prompt.submit({ text: 'next', wait: false, origin: { kind: 'composer' } })
    expect(await ui.find({ key: 'record' })).toBeUndefined()
    expect(w.ingests()).toHaveLength(0)
    await ui.unmount()
  })

  test('without a surface that draws the band, confirm records as auto does', { options: { capture: 'confirm' } }, async ($, on) => {
    const w = world(on, { surfaces: [] })
    await turn($, w, [EDIT])

    expect(w.ingests()).toHaveLength(1)
  })
})

describe('classic Stop of the kgai plugin', () => {
  test('the capture nudge is suppressed and auto-sync still runs', async ($, on) => {
    const w = world(on)
    const stop = await $.classic.Stop({ stop_hook_active: false })

    expect(w.seen.syncs).toBe(1)
    expect(stop.additionalContext).toBeUndefined()
    expect(stop.block).toBeUndefined()
  })

  test('another hook\'s context stays', async ($, on) => {
    world(on, { stop: { additionalContext: [NUDGE, 'from another plugin'] } })
    const stop = await $.classic.Stop({ stop_hook_active: false })

    expect(stop.additionalContext).toEqual(['from another plugin'])
  })

  test('the block form of the nudge is suppressed too', async ($, on) => {
    world(on, { stop: { block: NUDGE } })
    expect((await $.classic.Stop({ stop_hook_active: false })).block).toBeUndefined()
  })

  test('another block is kept', async ($, on) => {
    world(on, { stop: { block: 'not kgai' } })
    expect((await $.classic.Stop({ stop_hook_active: false })).block).toBe('not kgai')
  })
})

describe('parseAnswer', () => {
  test('reads NONE, payloads, fenced payloads and garbage', () => {
    expect(parseAnswer(' NONE ')).toEqual({ kind: 'none' })
    expect(parseAnswer(PAYLOAD)).toMatchObject({ kind: 'payload', title: '"Invoice renders standalone"' })
    expect(parseAnswer('```json\n' + PAYLOAD + '\n```')).toMatchObject({ kind: 'payload' })
    expect(parseAnswer('{"decisions":[{"title":"A"},{"title":"B"}]}')).toMatchObject({ kind: 'payload', title: '2 decisions' })
    expect(parseAnswer('I think nothing.')).toMatchObject({ kind: 'invalid' })
    expect(parseAnswer('{"decision":{}}')).toMatchObject({ kind: 'invalid' })
  })
})

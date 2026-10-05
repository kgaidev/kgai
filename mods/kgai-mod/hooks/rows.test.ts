import { describe, expect, test } from 'claude-code/testing'
import type { On } from 'claude-code'

import { bashRow, skillRow, titlesOf } from './rows'

const INGEST = `kg ingest <<'JSON'
{ "decision": { "title": "Project is named \\"test\\"", "rationale": "r", "mutations": [] } }
JSON`
const INGEST_ANSWER = { stdout: '{"ok": true, "decisions": [{"id": "d_1", "title": "Project is named test"}]}', stderr: '', interrupted: false }
const SEARCH_ANSWER = { stdout: '{"hits": []}', stderr: '', interrupted: false }

describe('which rows are compact', () => {
  test('reads, writes and sync by kg, by path or after env assignments', () => {
    expect(bashRow('kg search "invoice"', SEARCH_ANSWER)?.done).toBe('kgai: read the graph')
    expect(bashRow('~/.kgai/bin/kg context --paths "src/*"', SEARCH_ANSWER)?.running).toBe('kgai: reading the graph…')
    expect(bashRow('KGAI_HOME=/x kg resolve "project:test" 2>&1; kg query "MATCH (e) RETURN e" | head', SEARCH_ANSWER)?.done).toBe('kgai: read the graph')
    expect(bashRow('kg sync', undefined)?.done).toBe('kgai: synced the graph')
    expect(bashRow(INGEST, INGEST_ANSWER)?.done).toBe('kgai: recorded "Project is named test"')
    expect(bashRow(INGEST, undefined)?.running).toBe('kgai: recording the decision…')
    expect(bashRow('kg ingest --dry-run <<\'JSON\'\n{}\nJSON', undefined)?.done).toBe('kgai: checked the decision')
    expect(bashRow(`kg resolve "feature:A"; ${INGEST}`, { stdout: '{}\n{}' })?.done).toBe('kgai: recorded "Project is named "test""')
  })

  test('everything else keeps its full row', () => {
    expect(bashRow('go test ./...', undefined)).toBeUndefined()
    expect(bashRow('echo kg search', undefined)).toBeUndefined()
    expect(bashRow('kg init --actor "kgai maintainers"', undefined)).toBeUndefined()
    expect(bashRow('kg config set prompt x', undefined)).toBeUndefined()
    expect(skillRow('kgai:kg-trust')).toBeUndefined()
    expect(skillRow('kg-decision')).toBeUndefined()
    expect(skillRow('other:kg-ask')).toBeUndefined()
  })

  test('the kgai skills', () => {
    expect(skillRow('kgai:kg-decision')?.done).toBe('kgai: recording a decision')
    expect(skillRow('kgai:knowledge-graph')?.done).toBe('kgai: consulting the graph')
    expect(skillRow('kgai:kg-sync')?.done).toBe('kgai: syncing the graph')
  })

  test('titles from the answer, else from the payload', () => {
    expect(titlesOf(INGEST, INGEST_ANSWER)).toEqual(['Project is named test'])
    expect(titlesOf(INGEST, { stdout: 'not json' })).toEqual(['Project is named "test"'])
    expect(titlesOf('kg ingest < d.json', { stdout: '{"decisions":[{"title":"A"},{"title":"B"}]}' })).toEqual(['A', 'B'])
  })
})

// The engine's own drawing of a row no hook takes: here, a Box carrying the id.
const engine = (on: On) => on('ui.render', ($, e) => ({ type: 'Box', props: { key: 'engine' }, children: [] }))

const SURFACES = ['terminal', 'desktop', 'vscode', 'mobile'] as const

const toolUse = (tool: string, input: unknown, more: { isRunning?: boolean; isErrored?: boolean; output?: unknown } = {}) => ({
  component: 'ToolUse' as const,
  requestId: 'toolu_1',
  props: {
    tool_use_id: 'toolu_1',
    tool,
    input,
    isRunning: more.isRunning ?? false,
    isErrored: more.isErrored ?? false,
    isInterrupted: false,
    output: more.output,
  },
})

const toolResult = (tool: string, output: unknown, isErrored = false) => ({
  component: 'ToolResult' as const,
  requestId: 'toolu_1',
  props: { tool_use_id: 'toolu_1', tool, output, isErrored },
})

describe('drawing', () => {
  for (const surface of SURFACES) {
    test(`a kg ingest row is one line, its result left out, on ${surface}`, async ($, on) => {
      engine(on)
      const running = await $.ui.mount({ plugin: 'kgai-mod', surface, ...toolUse('Bash', { command: INGEST }, { isRunning: true }) })
      expect(await running.find({ type: 'Text', text: 'kgai: recording the decision…' })).toBeDefined()
      await running.unmount()

      const row = await $.ui.mount({ plugin: 'kgai-mod', surface, ...toolUse('Bash', { command: INGEST }, { output: INGEST_ANSWER }) })
      expect(await row.find({ type: 'Text', text: 'kgai: recorded "Project is named test"' })).toBeDefined()
      expect(await row.find({ text: 'ingest' })).toBeUndefined()
      await row.unmount()

      const result = await $.ui.mount({ plugin: 'kgai-mod', surface, ...toolResult('Bash', INGEST_ANSWER) })
      expect(await result.find({ key: 'engine' })).toBeUndefined()
      expect(await result.find({ text: 'decisions' })).toBeUndefined()
      await result.unmount()
    })
  }

  test('a kgai skill row is one line', async ($, on) => {
    engine(on)
    const row = await $.ui.mount({ plugin: 'kgai-mod', surface: 'terminal', ...toolUse('Skill', { skill: 'kgai:kg-decision' }, { output: {} }) })
    expect(await row.find({ type: 'Text', text: 'kgai: recording a decision' })).toBeDefined()
  })

  test('an errored kg call keeps its full row and result', async ($, on) => {
    engine(on)
    const row = await $.ui.mount({ plugin: 'kgai-mod', surface: 'terminal', ...toolUse('Bash', { command: INGEST }, { isErrored: true, output: 'refused' }) })
    expect(await row.find({ key: 'engine' })).toBeDefined()
    const result = await $.ui.mount({ plugin: 'kgai-mod', surface: 'terminal', ...toolResult('Bash', 'refused', true) })
    expect(await result.find({ key: 'engine' })).toBeDefined()
  })

  test('other tools are left alone, their results too', async ($, on) => {
    engine(on)
    const row = await $.ui.mount({ plugin: 'kgai-mod', surface: 'terminal', ...toolUse('Bash', { command: 'go test ./...' }, { output: SEARCH_ANSWER }) })
    expect(await row.find({ key: 'engine' })).toBeDefined()
    const result = await $.ui.mount({ plugin: 'kgai-mod', surface: 'terminal', ...toolResult('Bash', SEARCH_ANSWER) })
    expect(await result.find({ key: 'engine' })).toBeDefined()
  })

  test('compact: false leaves every row alone', { options: { compact: false } }, async ($, on) => {
    engine(on)
    const row = await $.ui.mount({ plugin: 'kgai-mod', surface: 'terminal', ...toolUse('Bash', { command: 'kg search x' }, { output: SEARCH_ANSWER }) })
    expect(await row.find({ key: 'engine' })).toBeDefined()
  })
})

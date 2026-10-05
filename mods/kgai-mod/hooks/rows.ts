// How kgai's own tool calls read in the transcript: one dim line each ("kgai: reading the
// graph", "kgai: recorded ...") instead of the command, its JSON payload and the engine's
// JSON answer. Only the drawing changes; the model and the stored transcript see it all.
//
// A call that errored keeps its full row, and so does anything that changes what kgai is
// allowed to do (kg init / config, the kg-trust skill): those the person should read.

// `kg <sub>` at the start of the command, by bare name or by path, after env assignments.
const KG_COMMAND = /^\s*(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)*(?:\S*\/)?kg\s+([a-z][\w-]*)/
const REAL_INGEST = /(?:^|[\s;&|(/])kg\s+ingest\b(?![^\n;&|]*--dry-run)/
const READS = new Set(['context', 'history', 'as-of', 'search', 'resolve', 'query', 'conflicts', 'status'])

// The kgai plugin's skills and commands, as the Skill tool names them.
const CONSULT_SKILLS = new Set(['knowledge-graph', 'kg-ask', 'kg-history', 'kg-query', 'kg-conflicts', 'kg-review'])

export type KgRow = { running: string; done: string }

const stdoutOf = (output: unknown) =>
  typeof output === 'object' && output !== null && typeof (output as { stdout?: unknown }).stdout === 'string'
    ? (output as { stdout: string }).stdout
    : ''

// The titles the engine answered with, else the ones in the payload the command carried.
export const titlesOf = (command: string, output: unknown): string[] => {
  try {
    const answer = JSON.parse(stdoutOf(output)) as { decisions?: { title?: unknown }[] }
    const titles = (answer.decisions ?? []).map(d => d.title).filter((t): t is string => typeof t === 'string')
    if (titles.length > 0) {
      return titles
    }
  } catch {
    // Not one JSON document (two commands, a log line): fall back to the payload.
  }
  return [...command.matchAll(/"title"\s*:\s*"((?:[^"\\]|\\.)*)"/g)].map(m => (m[1] ?? '').replace(/\\(.)/g, '$1'))
}

const recorded = (titles: string[]) =>
  titles.length === 1 ? `kgai: recorded "${titles[0]}"` : titles.length > 1 ? `kgai: recorded ${titles.length} decisions` : 'kgai: recorded the decision'

// The compact line for a Bash call, or undefined when the row stays as the engine draws it.
export const bashRow = (command: string, output: unknown): KgRow | undefined => {
  const sub = KG_COMMAND.exec(command)?.[1]
  if (sub === undefined) {
    return undefined
  }
  if (sub === 'ingest' || REAL_INGEST.test(command)) {
    return REAL_INGEST.test(command)
      ? { running: 'kgai: recording the decision…', done: recorded(titlesOf(command, output)) }
      : { running: 'kgai: checking the decision…', done: 'kgai: checked the decision' }
  }
  if (sub === 'sync') {
    return { running: 'kgai: syncing the graph…', done: 'kgai: synced the graph' }
  }
  if (READS.has(sub)) {
    return { running: 'kgai: reading the graph…', done: 'kgai: read the graph' }
  }
  return undefined
}

// The compact line for a Skill call of the kgai plugin, or undefined.
export const skillRow = (skill: string): KgRow | undefined => {
  const name = /^kgai:(.+)$/.exec(skill)?.[1]
  if (name === 'kg-decision') {
    return { running: 'kgai: recording a decision…', done: 'kgai: recording a decision' }
  }
  if (name === 'kg-sync') {
    return { running: 'kgai: syncing the graph…', done: 'kgai: syncing the graph' }
  }
  if (name !== undefined && CONSULT_SKILLS.has(name)) {
    return { running: 'kgai: consulting the graph…', done: 'kgai: consulting the graph' }
  }
  return undefined
}

// The compact line for any tool row, from what the ToolUse / ToolResult sites carry.
export const kgRow = (tool: string, input: unknown, output: unknown): KgRow | undefined => {
  const field = (key: string) =>
    typeof input === 'object' && input !== null && typeof (input as Record<string, unknown>)[key] === 'string'
      ? ((input as Record<string, unknown>)[key] as string)
      : undefined
  if (tool === 'Bash') {
    const command = field('command')
    return command === undefined ? undefined : bashRow(command, output)
  }
  if (tool === 'Skill') {
    const skill = field('skill')
    return skill === undefined ? undefined : skillRow(skill)
  }
  return undefined
}

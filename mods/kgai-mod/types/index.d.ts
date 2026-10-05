// A decision the side check proposed in `confirm` mode, waiting for Record / Skip.
export type KgaiModPending = { title: string; payload: string }

declare module 'claude-code' {
  interface PluginState {
    'kgai-mod': {
      pending: KgaiModPending | null
      hasToastedFailure: boolean
    }
  }
}

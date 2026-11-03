// Context window breakdown of a session (AgentService.GetContextUsage) as plain data for the details pane.
// The client computes it: provider token counts where known, ~4 characters per token elsewhere.

import { ContextUsage_Category, type ContextUsage } from '~/gen/harness/agent/v1/agent_pb'

export type ContextCategoryKey = 'messages' | 'mcp_tools' | 'system_tools' | 'system_prompt' | 'skills' | 'mcp_instructions' | 'buffer' | 'free'

export interface ContextCategory {
  key: ContextCategoryKey
  label: string
  tokens: number
}

export interface ContextMcpTool {
  server: string
  tool: string
  name: string
  description: string
  tokens: number
  // Only its name is in the context until ToolSearch loads it.
  deferred: boolean
}

export interface ContextUsageVM {
  model: string
  window: number
  used: number
  // In display order; they add up to the window.
  categories: ContextCategory[]
  deferredMcpTools: number
  deferredSystemTools: number
  mcpTools: ContextMcpTool[]
  // 0 when auto-compaction is off.
  autocompactThreshold: number
  untilAutocompact: number
  estimated: boolean
  compacting: boolean
}

const CATEGORY: Partial<Record<ContextUsage_Category, { key: ContextCategoryKey, label: string }>> = {
  [ContextUsage_Category.MESSAGES]: { key: 'messages', label: 'Messages' },
  [ContextUsage_Category.MCP_TOOLS]: { key: 'mcp_tools', label: 'MCP tools' },
  [ContextUsage_Category.SYSTEM_TOOLS]: { key: 'system_tools', label: 'System tools' },
  [ContextUsage_Category.SYSTEM_PROMPT]: { key: 'system_prompt', label: 'System prompt' },
  [ContextUsage_Category.SKILLS]: { key: 'skills', label: 'Skills' },
  [ContextUsage_Category.MCP_SERVER_INSTRUCTIONS]: { key: 'mcp_instructions', label: 'MCP server instructions' },
  [ContextUsage_Category.AUTOCOMPACT_BUFFER]: { key: 'buffer', label: 'Autocompact buffer' },
  [ContextUsage_Category.FREE_SPACE]: { key: 'free', label: 'Free space' }
}

const CHART = ['bg-chart-1', 'bg-chart-2', 'bg-chart-3', 'bg-chart-4', 'bg-chart-5']

// Series colours, one per category: the content categories take the chart tokens in display order (so they stay
// fixed: messages, MCP tools, system tools, system prompt, then skills or server instructions); the buffer and
// free space stay neutral. Text never wears these.
export function categorySwatches(categories: ContextCategory[]): string[] {
  let i = 0
  return categories.map((c) => {
    if (c.key === 'buffer') return 'bg-dimmed'
    if (c.key === 'free') return 'bg-raised'
    return CHART[i++ % CHART.length]!
  })
}

export function contextUsageFromProto(u: ContextUsage): ContextUsageVM {
  const categories: ContextCategory[] = []
  for (const e of u.categories) {
    const c = CATEGORY[e.category]
    if (c) categories.push({ ...c, tokens: Number(e.tokens) })
  }
  return {
    model: u.model,
    window: Number(u.contextWindow),
    used: Number(u.usedTokens),
    categories,
    deferredMcpTools: Number(u.deferredMcpToolsTokens),
    deferredSystemTools: Number(u.deferredSystemToolsTokens),
    mcpTools: u.mcpTools.map(t => ({ server: t.server, tool: t.tool, name: t.name, description: t.description, tokens: Number(t.tokens), deferred: t.deferred })),
    autocompactThreshold: Number(u.autocompactThreshold),
    untilAutocompact: Number(u.tokensUntilAutocompact),
    estimated: u.estimated,
    compacting: u.compacting
  }
}

// Percent of the window, rounded; 0 when the window is unknown.
export function percentOf(tokens: number, window: number) {
  return window > 0 ? Math.round((tokens / window) * 100) : 0
}

// An MCP tool row's label: "Server · Tool description" (the description's first line, or the tool name).
export function mcpToolLabel(t: ContextMcpTool) {
  const desc = t.description.split('\n')[0]!.trim()
  return `${t.server} · ${desc || t.tool}`
}

/** `rules` provider: the deterministic engine in rules.ts behind the DecisionProvider interface. */
import { evaluateRules, type RulesOptions } from "./rules.ts";
import type { DecideInput, DecisionProvider, ProviderDecision } from "./types.ts";

export class RulesProvider implements DecisionProvider {
  readonly id = "rules";
  readonly type = "rules" as const;

  constructor(
    private readonly options: RulesOptions = {},
    readonly displayName = "Built-in rules",
  ) {}

  async decide(input: DecideInput): Promise<ProviderDecision> {
    const r = evaluateRules(
      {
        tool_name: input.toolName,
        input: input.input,
        cwd: input.cwd,
        additional_directories: input.additionalDirectories,
        facts: {
          subcommands: input.facts.subcommands,
          write_paths: input.facts.writePaths,
          network_hosts: input.facts.networkHosts,
          inside_git_repo: input.facts.insideGitRepo,
          git_branch: input.facts.gitBranch,
        },
      },
      this.options,
    );
    return { decision: r.decision, reason: r.reason, categories: r.categories };
  }

  async healthy(): Promise<boolean> {
    return true;
  }
}

# Handoff: knowledge sources split from issue trackers

Branch `worktree-agent-a58b2378f3a1713cd`. The design is in `docs/design/tracker.md` (sections 1, 6, 8 and 10, "Writing a provider").

## Goal

Issue trackers and Knowledge sources as two separate kinds of connection (device and company scope each), knowledge sources able to
borrow a compatible tracker connection's sign-in, YouTrack knowledge migrated onto that, reserved proto pieces, provider registries on
both sides, a capability-driven UI, and a design doc provider agents can follow literally.

## Done

- `4a9cf52d` Proto: `knowledge_connections.proto` (`KnowledgeConnectionService`, `KnowledgeCapabilities`, `KnowledgeConnection`,
  `KnowledgeConnectionInput`), `*LocalKnowledge*` RPCs on `LocalTrackerService`, reserved numbers (table in design doc section 8).
- `54bfc7ac` Client: `tracker/knowledge.go` (interface, `RegisterKnowledge`), `knowledge_store.go` (store, borrowing, `MigrateKnowledge`),
  `knowledge_service.go` (protos, merged Knowledge view), `signin.go` (generic checks, `CheckOptions`, `DeviceVCSToken`),
  `tracker/providers/providers.go` (registry), YouTrack `knowledge.go` (`youtrack-kb`), server handlers, tests.
- `c8536a72` Core: `tracker/registry.ts`, generic `tracker/connections.ts`, `tracker/signin.ts`, `tracker/vcs-signin.ts`,
  `tracker/oauth-state.ts`, `knowledge/{provider,connections,youtrack-kb}.ts`, `rpc/knowledge-connections.ts`, `rpc/provider-convert.ts`,
  migration `0018_knowledge_connections` (schema + data), tests (`db.test.ts`, `issues.test.ts`, `provider-registry.test.ts`).
- `d0c05ff7` UI: `SettingsKnowledgeSources.vue`, shared `SettingsProviderSignIn.vue`, capability-driven `SettingsTrackers.vue`,
  IssuesApi knowledge methods (connect + mock), i18n extracted.
- `53631b65` Design doc. `4941951f` keep legacy flags on save until migrated. `3908f86c` merge of origin/main (catalog.json re-extracted).

## Checks (all green after the last merge)

gofmt/go vet; `go test ./tracker/... ./server/...`; core vitest (982 passed); UI eslint on changed files, `nuxt typecheck`, vitest (570).

## Not done, in order

1. Visual check of Settings > Knowledge sources and Issue trackers in the app (not run: no preview config in this worktree).
2. Form option for an own address with a borrowed sign-in (API supports it).
3. A picker for `vcs_connection_id` instead of a plain option field.
4. The providers themselves (Jira, Linear, GitHub/GitLab Issues, Yandex Tracker, Kaiten; Confluence, Yandex Wiki, Linear docs,
   GitHub/GitLab wikis, Kaiten docs), one agent per vendor, per design doc section 10.

## Decisions

- Knowledge interface and store live in Go package `tracker` (avoids an import cycle with the merged view used by tools).
- Document ids stay `conn:<knowledge connection id>:<article>`; legacy `conn:<tracker id>:...` resolve via `migrated_from`.
- Borrowing is same-scope only, resolved at every open, never copied; refreshed tokens go back to the tracker connection.
- Removing/disabling a lent tracker connection does not remove the source; it fails visibly.
- DB columns `knowledge_enabled`/`knowledge_query` kept (always false); proto fields deprecated, numbers not reused.
- Company and device groups in Knowledge sources list each scope's own providers.

## Gotchas

- Worktree has no node_modules: symlinks to the main checkout's `packages/{core,ui}/node_modules` were made (ignored by git).
- Another agent's migration may also take number 0018: on merge, renumber ours (drizzle journal + snapshot) and re-run
  `node scripts/embed-migrations.ts`.
- `catalog.json` conflicts: take theirs, then `node scripts/i18n-extract.mjs --apply` in packages/ui.
- eslint in packages/ui needs `nuxt prepare` first.

## Next steps for another agent

`git fetch origin && git merge origin/main`, run the checks above, do item 1, then hand section 10 of the design doc to provider agents.

<!-- Generated from packages/client/settings/policy.schema.json by scripts/policy-templates.sh. Don't edit it here: change the schema and run the script. -->

# Machine policy keys

Every key an administrator can set on a machine, from `packages/client/settings/policy.schema.json`. On macOS they are keys of the preference domain `com.xlsft.harness`, nested as shown (`sandbox.enabled` is `enabled` in a `sandbox` dictionary). On Windows a single value is a registry value named by the whole path under `HKLM\SOFTWARE\Policies\xlsft\harness`; lists and dictionaries go in the `Settings` value as JSON. In `settings.json` and `settings.d/*.json` of the system folder they are ordinary JSON.

**machine** keys configure the installation and are read only from these places. **policy** keys are the same as in the server's managed policy and have its force.

| Key | Scope | Type | Windows | Template | Description |
|---|---|---|---|---|---|
| `allowManagedHooksOnly` | policy | boolean | REG_DWORD |  | Only the hooks of policies run. |
| `allowManagedPermissionRulesOnly` | policy | boolean | REG_DWORD |  | Permission rules from user, project and local settings are ignored. |
| `allowedMcpServers` | policy | array of string | JSON in `Settings` |  | Only MCP servers with these names may be used, at any scope. An empty list allows none. |
| `allowedProviders` | policy | array of string | JSON in `Settings` |  | Only these providers (id or preset id) serve models. An empty list allows none. |
| `allowedSkills` | policy | array of string | JSON in `Settings` |  | Only skills of these names may run; "kit:*" names all of a plugin. An empty list allows none. |
| `blockedMarketplaces` | policy | array of object | JSON in `Settings` |  | Plugin sources that are refused, in the same forms, checked first. |
| `blockedSkills` | policy | array of string | JSON in `Settings` |  | Skills that may not run, checked before allowedSkills. |
| `disableAllHooks` | policy | boolean | REG_DWORD |  | No hooks run, from any level. |
| `disableExternalSkillSearch` | policy | boolean | REG_DWORD |  | No search of public skill directories. |
| `disableLocalModels` | policy | boolean | REG_DWORD |  | Models running on this machine can't be used. |
| `disablePersonalProviders` | policy | boolean | REG_DWORD |  | Users can't add or use their own model providers. |
| `disableUpdates` | machine | boolean | REG_DWORD | `false` | The desktop app and `harness-client update` don't look for or install updates. Use it when you deploy new versions yourself, for example as a package through device management. |
| `enabledPlugins` | policy | object | JSON in `Settings` |  | name@marketplace: true installs and keeps it on, false blocks it at every level. |
| `permissions.allow` | policy | array of string | JSON in `Settings` |  | Permission rules, e.g. Bash(npm test *). |
| `permissions.ask` | policy | array of string | JSON in `Settings` |  | Permission rules that always ask, e.g. Bash(git push *). |
| `permissions.defaultMode` | policy | string: default, acceptEdits, plan, dontAsk, bypassPermissions, auto | REG_SZ |  | The mode new sessions start in. |
| `permissions.deny` | policy | array of string | JSON in `Settings` |  | Permission rules that are always denied, e.g. Read(./.env). |
| `permissions.disableAutoMode` | policy | string: disable | REG_SZ |  | "disable": nobody can switch to auto mode. |
| `permissions.disableBypassPermissionsMode` | policy | string: disable | REG_SZ | `"disable"` | "disable": nobody can switch to bypassPermissions. |
| `permissions.disabledModes` | policy | array of string | JSON in `Settings` |  | Permission modes nobody may use. The default mode can't be turned off. |
| `remoteControl` | policy | boolean | REG_DWORD |  | false: sessions are never mirrored to the server for watching and control from the web. |
| `sandbox.allowOutside` | policy | boolean | REG_DWORD |  | false: commands can't run outside the sandbox at all. |
| `sandbox.enabled` | policy | boolean | REG_DWORD |  | true: the sandbox can't be turned off. |
| `serverUrl` | machine | string | REG_SZ | `"https://harness.example.com"` | The harness server this machine signs in to. The desktop app's sign-in screen uses it and can't change it, and `harness-client login` takes it when no URL is given. |
| `strictKnownMarketplaces` | policy | array of object | JSON in `Settings` |  | Plugins and marketplaces come only from these sources. An empty list allows none. |
| `updateMirrorUrl` | machine | string | REG_SZ |  | Where updates come from instead of GitHub: a folder with latest.json and one subfolder per release tag, as scripts/release/offline-bundle.sh makes it. Updates are still verified with the release key built into the app and the client. |
| `webSearchMode` | policy | string: live, cached, disabled | REG_SZ |  | live, cached, or disabled for the WebSearch tool. |

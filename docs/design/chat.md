# Chat apps and phone dispatch

Two rows of `docs/comparison.md`: "Chat app surface (Slack etc.)" and "Message from phone that spawns a desktop
session". Both start a session on one of the user's own machines from somewhere else, so they share one mechanism: a
**remote start**, the relay's `SpawnSession` command.

## Remote starts

- A machine takes remote starts only when its owner turned them on there: the device setting `allow_remote_starts`
  (`DeviceSettings`, in `HARNESS_HOME/device-settings.json`). Off by default. It is device-local: it is never synced,
  never sent to the server as a setting, and changing it on one machine says nothing about another.
- The host tells the relay through `RegisterHostRequest.remote_starts`. Toggling the setting re-registers the host
  (same instance id, so the same host id) so viewers see the change at once.
- Folders: a remote start may only use one of the machine's recent folders (`ListRecentFolders`, the folders of its
  recent sessions), exactly, after cleaning the path. The host checks this itself; the server cannot see the list
  and does not try to. Viewers read the list live with the relay query command `list_recent_folders` (Command 280),
  which a host answers only while remote starts are on. Daemon hosts (`Options.Daemon`, the always-on kind) keep
  their own rule: anything under their allowed roots.
- The mode is chosen by the sender. `bypassPermissions` is refused by the server and by the host, as for every relay
  command, and the host's mode policy applies as for a local start.
- The server refuses a spawn to a host that neither is a daemon nor allows remote starts, and every spawn while the
  organization's policy turns remote starts off (`disableRemoteStarts`). The client also stops advertising remote
  starts under that policy. Every spawn attempt is audited (`relay.remote_start`, success or failure, with machine
  and frontend; the folder is recorded, the message is not).

## Phone dispatch

The web UI on a phone (the existing narrow layouts) gets a "Start on a machine" sheet: machine (online hosts that take
remote starts), folder (that machine's recent folders, asked live), mode, and the first message. The UI only calls
RPCs: `RelayService.ListHosts` for machines, `RelayService.ListHostFolders` for folders, `RelayService.SpawnSession`
to start, all forwarded by the client's core proxy. The session then shows up as a remote session of that machine.

## Chat apps

### Provider interface

`packages/core/src/chat/types.ts` defines `ChatProvider`: request verification, parsing inbound requests into
`ChatInbound` items (a message, a button press, a URL check), and the outbound calls (post, update, ephemeral notice,
user lookup, token check). `packages/core/src/chat/service.ts` holds everything that is not provider-specific: linking,
choosing a machine, mapping threads to sessions, following a session and rendering its progress. Slack is the only
provider built (`chat/slack.ts`); Teams adds a `ChatProvider` and a route, no change to the service.

### Connecting an app

An admin enters the bot token and signing secret under Settings > Chat apps. They are stored as one AES-256-GCM blob
(HKDF purpose `chat-secrets`, AAD = app id), write-only, never logged and never returned. Saving checks the token
with `auth.test` and records the workspace (team id and name) and the bot user. One Slack app per server.

### Linking a Slack identity

- **Directory**: when an unlinked Slack user writes to the bot, the server looks up their Slack profile email
  (`users.info`) and links them to the account with exactly that email when the account comes from the directory
  (LDAP, SSO or SCIM) or has a verified email. The admin can turn this matching off.
- **One-time code**: the user creates a code in Settings > Chat apps (single use, 15 minutes, stored hashed) and sends
  `link <code>` to the bot. The code is made on the harness side and typed in Slack, not the other way round:
  someone who tricks a person into using a code then links *their own* account to the other person's Slack, which
  only lets that person start sessions on the trickster's machines. The reverse direction would give the trickster
  control of the victim's machines.
- Links are personal: a user sees and removes their own; there is no admin listing of them.

### Threads and sessions

- Mentioning the bot in a channel or writing to it in a DM starts a session; the reply thread is bound to it
  (`chat_thread`). Later messages in the thread from the session's owner are sent to the session as user messages;
  other people's replies are ignored.
- Where it runs: the machine, folder and mode in the user's chat defaults (`UserPreferences.chat_defaults`, field
  44, synced as `chatDefaults` in the user's settings document) when that machine is online and takes remote starts;
  otherwise the most recently seen online host that takes remote starts (a daemon host counts), in its most recent
  folder, in the default mode.
- The server follows the session through the relay's own history-then-live subscription, from the last seq it
  handled, so a server restart picks up where it stopped. The thread gets one progress message that is edited (tool
  summaries, throttled), permission prompts as buttons, and the final answer of each turn.
- Buttons are answered only by the session's owner (the clicking Slack user must be linked to the owner); anyone
  else gets an ephemeral "only the owner can answer". The answer goes through the relay like a phone's would, so the
  first answer wins everywhere.

### Request verification

Every request must carry a valid `X-Slack-Signature` (`v0`, HMAC-SHA256 of `v0:<timestamp>:<body>` with the signing
secret) and an `X-Slack-Request-Timestamp` within five minutes. A signature seen before is refused as a replay. Slack's
own retries of an event carry a new signature and the same `event_id`; they are answered 200 and not processed again.

### Policy and audit

`disableChatApps` in the managed policy (ManagedPolicy 280) turns the integration off for everyone: inbound requests
are answered with a short notice and start nothing. `disableRemoteStarts` (281) stops remote starts, which also stops
Slack from starting sessions. Audited: connecting and removing the app, linking and unlinking (without the code), and
each session a chat thread starts.

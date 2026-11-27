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
choosing a machine, mapping threads to sessions, following a session and rendering its progress. Two providers are
built, Slack (`chat/slack.ts`) and Telegram (`chat/telegram.ts`, see below); Teams adds a `ChatProvider` and a route,
no change to the service.

### Connecting an app

An admin enters the bot token and signing secret under Settings > Chat apps. They are stored as one AES-256-GCM blob
(HKDF purpose `chat-secrets`, AAD = app id), write-only, never logged and never returned. Saving checks the token
with `auth.test` and records the workspace (team id and name) and the bot user. One Slack app per server.

### Linking a Slack identity

- **Directory**: when an unlinked Slack user writes to the bot, the server looks up their Slack profile email
  (`users.info`) and links them to the account with exactly that email when the account comes from the directory
  (LDAP, SSO or SCIM) or has a verified email. The admin can turn this matching off.
- **One-time code**: the user creates a code in Settings > Remote (single use, 15 minutes, stored hashed) and sends
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
  44, synced as `chatDefaults` in the user's settings document, key `user`) when that machine is online and takes
  remote starts; otherwise the most recently seen online host that takes remote starts (a daemon host counts), in its
  most recent folder, in the default mode. When the defaults name an always-on host (`agent_host_id` and
  `repository_key`), the session is queued there instead (`StartHostWork`, see docs/design/hosts.md) and the thread
  waits for the work's session, then follows it as any other; the wait survives a server restart (`host_work_id`).
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

### What the model cannot do

The remote start switch is not a `SettingsSet` key, and `UpdateDeviceSettings` refuses to change it from a relay
origin: only a frontend on the machine itself turns it on.

## Telegram

Range 380 (ManagedPolicy 380). `packages/core/src/chat/telegram.ts` (provider), `telegram-delivery.ts` (webhook or
polling), `buttons.ts` (button values kept on the server), `test/telegram.test.ts` (a fake Bot API).

### Decisions

1. **Same provider interface, one new knob.** Telegram is a `ChatProvider` like Slack. What differs is threading: a
   private chat has no threads, so `ChatProvider.threading` is `"chat"`: the latest live session started in a chat is
   the one later messages steer, until `/new <task>` starts another. The earlier one is marked `detached` and keeps
   running on its machine; the chat stops following it. Replying to an older message does not pick an older session
   (that would need every bot message mapped to its session; not worth it for a phone chat).
2. **Private chats only.** Group and channel updates are ignored: permission prompts and answers belong to one person,
   and in a group everyone would see them. Buttons still check who pressed (see 6).
3. **The organization's bot is entered by an admin** (Settings > Chat apps), checked with `getMe`, stored like the Slack
   app's secrets (one AES-256-GCM blob, HKDF `chat-secrets`). Telegram has no signing secret; the server makes a random
   32-byte secret token itself, stores it in the same blob and gives it to `setWebhook`. A new bot token gets a new
   secret token. Neither is ever returned or logged; Telegram errors are reported by method name, never with the URL
   (the token is part of every Bot API URL).
4. **A person may bring their own bot** (`PutMyChatBot`, Settings > Remote), stored per owner (`chat_bot`, AAD
   `chat:<provider>:bot:<id>`), visible and changeable by the owner only: there is no admin listing. A personal bot
   serves its owner alone: a link code redeemed through it must be the owner's, and a message from anyone else gets
   "this bot belongs to someone else". One bot is either the organization's or one person's, never both. Removing the
   bot removes the links made through it. The managed policy can turn personal bots off (`disablePersonalChatBots`,
   ManagedPolicy 380, server-enforced): saving is refused, and an existing personal bot answers with a notice.
5. **Updates by webhook, or long polling as a fallback.** `ChatDelivery`: automatic (a webhook when the public URL is
   HTTPS, which Telegram requires), webhook, or polling. The webhook is `POST /api/chat/telegram/webhook/{org|<bot id>}`
   and must carry `X-Telegram-Bot-Api-Secret-Token`, compared in constant time. Polling is `getUpdates` in the server
   process, one loop per bot, started on save and on server start (`resumeTelegramDelivery`); Telegram refuses
   concurrent polls of one bot, so polling assumes one replica, like the watchers. Offsets are in memory: after a
   restart Telegram hands out what it still holds, and update ids (`chat_delivery`, keyed by a hash of the bot's secret
   token and the update id) keep any update from being handled twice, whichever way it arrived. An app or bot that is
   turned off keeps receiving updates, so people get a notice instead of silence.
6. **Buttons carry an id, not the value.** `callback_data` is 64 bytes; the service's button value (thread id, request
   id, decision) is stored in `chat_button` and the button carries `cb:<id>`. An id is not a capability: a press is
   answered only when the presser's Telegram account is linked to the session's owner and the press comes from the
   thread's own chat. Every press is acknowledged (`answerCallbackQuery`), with the "only the owner can answer" notice
   when it is someone else's. The prompt is edited to its outcome and loses its keyboard.
7. **Linking.** The code is made in Settings > Remote, as for Slack. For Telegram the response also carries a deep link
   (`https://t.me/<bot>?start=<code>`): opening it sends `/start <code>`, which the provider turns into `link <code>`.
   `/link <code>` works too. Telegram shares no email, so there is no directory matching for it.
8. **Progress** is one message, edited (`editMessageText`); long answers are split at line ends into messages of at
   most 4096 characters; no Markdown parsing, so nothing in an answer can break the message.
9. **Commands**: `/start` and `/help` explain, `/start <code>` and `/link <code>` link, `/new <task>` starts another
   session; anything else is a task or, while a session is live, a message to it.

### Not built

Groups and forum topics, answering `AskUserQuestion` from the chat (it points to the app), voice and files (a photo's
caption is used as text, the photo is not), choosing a machine per message, a multi-replica poller.

### Status

Tested against fake Slack Web and Telegram Bot APIs and fake relay hosts only (`packages/core/test/chat.test.ts`,
`packages/core/test/telegram.test.ts`, `packages/client/relay/remotestart_test.go`). No real Slack workspace or
Telegram bot was used. Not built yet: a Teams provider, answering `AskUserQuestion` and plans from the thread (the
thread points to the app instead), slash commands, a server-rendered link page (linking is by code from Settings >
Remote), and a multi-replica bus for the watchers (one core replica follows all threads).

Fixed on the way: the server read chat defaults from the user settings document under key `settings`, and the client
wrote preferences there; the server takes only `user` for that document, so neither worked against a real server.
Both use `user` now (`settings.UserSettingsKey`).

### Policy and audit

`disableChatApps` in the managed policy (ManagedPolicy 280) turns the integration off for everyone, Slack and Telegram,
the organization's bots and personal ones: inbound requests are answered with a short notice and start nothing.
`disableRemoteStarts` (281) stops remote starts, which also stops chat apps from starting sessions on machines.
`disablePersonalChatBots` (380) turns personal Telegram bots off. Audited: connecting and removing the app
(`chat.app_update` with whether the token changed, `chat.app_delete`), saving and removing a personal bot
(`chat.bot_update`, `chat.bot_delete`), linking (`chat.link`, by code or directory, never the code) and unlinking
(`chat.unlink`), and each session a chat starts (`chat.session_start`: the session, or the always-on host's work; the
message itself is not recorded). `bypassPermissions` is never granted from a chat: the mode names it cannot map to
that mode, the client refuses it as a chat default, and the relay and the always-on hosts refuse it again.

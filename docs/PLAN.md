# harness — план реализации

> Харнесс на замену Claude Code: те же инструменты, тот же UX, та же архитектура, любые модели через OpenAI- и Anthropic-совместимые эндпоинты.
> Стек UI: Nuxt 4 + Nuxt UI v4. Основа: 16 документов из `docs/research` репозитория xlsft/edualc (данные на октябрь 2026, CC 2.1.289).

## Модель продукта: как Claude Code + свой бэкенд вместо Anthropic

| Claude Code | harness |
|---|---|
| `claude` локально: агентный цикл, исполнение инструментов, permissions, hooks, stdio-MCP | **`harness` (клиент-раннер)** локально: то же самое. Исполняет tool calls в своей ФС |
| Логин через claude.ai, модели через API Anthropic, ключей у пользователя нет | Логин на **harness server**. Модели — через **LLM-gateway сервера**, ключи провайдеров только на сервере |
| Системный промпт и описания инструментов приходят с релизами CLI | **Промпты живут на сервере** (версионируемый prompt bundle) и синхронизируются на машины |
| Аккаунт-данные, managed settings, сессии на claude.ai | **Все пользовательские данные на сервере** (настройки, memory, skills, agents, сессии…), клиент подтягивает и кэширует |
| Remote Control: CLI → relay → claude.ai/code и мобильный | Тот же relay в harness server + Web UI + туннели |
| Desktop запускает CLI подпроцессом, SSH ставит CLI на remote | То же самое |

**Принцип:** клиент остаётся клиентом. Локально живут только исполнение (shell, ФС, git, stdio-MCP) и кэш. Всё «бизнесовое» — auth, ключи, модели, промпты, данные пользователя, история, usage — на сервере. Docker-контейнер сервера — единая точка входа к этой части.

```
┌────────────────────────── harness server (Docker) ──────────────────────────┐
│ auth · LLM-gateway (ключи, роутинг, адаптеры провайдеров, usage, квоты)     │
│ prompt registry · user data (settings, memory, skills, agents, MCP, сессии)  │
│ relay / remote control · туннели · Web UI (Nuxt)                             │
└──────▲──────────────────────▲───────────────────────────▲───────────────────┘
       │ HTTPS: gateway API,  │ relay (исходящее          │ WS/SSE
       │ sync API             │ от клиента)               │
┌──────┴──────── машина пользователя ─────────────────────┐   Browser/телефон
│ harness: агентный цикл · исполнение tools · permissions │   (Web UI на сервере,
│         hooks · stdio-MCP · кэш данных и промптов      │    управляет сессиями
│   ▲ TUI (тот же процесс)     ▲ stdio NDJSON              │    через relay)
│   терминал             Desktop UI (Nuxt UI в оболочке)   │
│                              │ ssh (ключи клиента)        │
└──────────────────────────────┼───────────────────────────┘
                               ▼
                    remote: harness по stdio через SSH
```

---

## 0. Ключевые решения

| # | Решение | Выбор | Почему |
|---|---|---|---|
| A1 | Разделение | **Клиент:** цикл, исполнение tools, permissions, hooks, stdio-MCP, кэш. **Сервер:** auth, LLM-gateway с ключами, промпты, все пользовательские данные, relay, туннели, Web UI | Как CC + Anthropic-бэкенд. Ключи и бизнес-логика никогда не попадают на машины |
| A2 | LLM-gateway | Клиент говорит с сервером **одним каноническим API** (Anthropic-Messages-подобный, стрим SSE). Сервер транслирует в upstream: Anthropic / OpenAI-chat / OpenAI-responses | Адаптеры, capability negotiation, кэш-брейкпоинты, ретраи и fallback живут в одном месте. Новый провайдер или модель не требуют обновления клиентов |
| A3 | Промпты | **Prompt registry на сервере:** системный промпт по секциям, описания инструментов, шаблоны reminders, промпт компактора, bundled skills, **оверрайды под модель**. Версионируются, клиент кэширует по ETag | Промпт-инжиниринг под разные модели без релиза клиента. Можно A/B и откаты. В кэше клиент работает при кратком офлайне |
| A4 | Пользовательские данные | **Сервер — источник правды**, клиент — кэш с синхронизацией (версии документов, push локальных изменений) | «Клиент подтягивает всё с сервера». Одинаковое окружение на любой машине |
| A5 | Протокол клиент-раннера | **SDK-протокол CC-формы** (NDJSON): `system/assistant/user/stream_event/result` + `control_request/control_response` | Один протокол для desktop (stdio), headless, SDK и как payload поверх relay |
| A6 | Relay | Исходящее соединение клиента к серверу (SSE + POST либо WS), envelope `{seq, sessionId, epoch, uuid, type, payload}` | Работает за NAT. Паттерны CC Remote Control: epoch, dedup, FlushGate, heartbeat |
| A7 | UI | Одно Nuxt-приложение `packages/ui` для desktop и mobile (и веба) с `SessionTransport`: `ipc` (desktop → локальный Go-клиент по stdio), `relay` (web/mobile → сервер → клиент на машине) | Все GUI-клиенты выглядят одинаково, как у CC |
| A8 | Каноническая модель сообщений | **Anthropic-подобные content blocks** (`text`, `thinking`+`signature`, `tool_use`, `tool_result`, `image`) — и в клиенте, и в gateway API | Сводить богатое к бедному проще. Пары tool_use/tool_result и подписи thinking соблюдаются |
| A9 | Auth | **Better Auth** на сервере (логин, пароль, опционально OAuth и 2FA, роли). `harness login` — device-flow → токен устройства (отзывается) + короткоживущий access-токен с refresh | Обычный клиентский логин, как `claude login` |
| A10 | Хранение | **Сервер:** SQLite по умолчанию / Postgres опционально (Drizzle) + каталог для вложений. **Клиент:** кэш в `~/.harness/` + локальный JSONL CC-формата (запись офлайн, синк на сервер) | `docker compose up` без зависимостей. Сессия не теряется при обрыве связи |
| A11 | Совместимость с CC | Проектные `.claude/`, `CLAUDE.md`, `AGENTS.md`, `.mcp.json` читаются из репозитория. Пользовательский `~/.claude` — разовый **импорт на сервер** (`harness import claude`) | Проект переносим. Пользовательский уровень после импорта живёт на сервере |
| A12 | Языки и дистрибуция | **Сервер** — TypeScript на Nitro v3. **Клиент и CLI** — Go: один статический бинарь под все ОС и архитектуры (cross-compile без CGO). CLI импортирует клиент как Go-пакет. Desktop и mobile используют клиент как sidecar-бинарь | Один файл легко ставить и заливать по SSH, быстрый старт, нормальные process/pty-примитивы. тулчейн charm.land даёт TUI уровня CC |
| A13 | Право | Clean-room: совместимые форматы и протоколы — да, код и промпты CC дословно — нет | См. feature-parity §17–18 |

### Структура репозитория

Монорепо **без монорепо-тулинга**: нет pnpm workspaces, turbo и общих lock-файлов. Каждый пакет самостоятельный, со своим `package.json` или `go.mod`, своей сборкой и своим CI-джобом. Общий контракт между TS и Go — **спецификация в `docs/protocol/`** (markdown + JSON Schema), а не общий код.

```
docs/
  research/        # исследования (копия из xlsft/edualc при необходимости)
  protocol/        # контракты: gateway API, sync API, SDK-протокол (stdio NDJSON), relay-envelope
  PLAN.md
packages/
  core/            # сервер: Nitro v3 (nitro.build), TypeScript. Auth, LLM-gateway + адаптеры провайдеров,
                   #   prompt registry, user data + sync, MCP-реестр и прокси, relay, туннели. Dockerfile
  client/          # универсальный клиент для фронтендов, Go. Агентный цикл, исполнение tools, permissions,
                   #   hooks, stdio-MCP, сессии (JSONL, checkpoints), sync-кэш, gateway- и relay-клиент.
                   #   Отдаётся как Go-пакет (для cli) и как бинарь `harness-client` со stdio NDJSON (для ui/desktop, SSH)
  ui/              # фронтенд для desktop и mobile: Nuxt 4 + Nuxt UI v4 (дизайн будет отдельно)
  cli/             # CLI-фронтенд, Go: весь тулчейн charm.land (Bubble Tea, Bubbles, Lip Gloss, Glamour,
                   #   Huh, Harmonica, Log…). Truecolor с fallback на ANSI-16/256
                   #   для эмуляторов без поддержки. Дизайн 1:1 с Claude Code. Бинарь `harness`
```

**Порядок работ:** сначала `core` + `client` (весь харнесс работает headless, проверяется тестами и `harness-client -p`), затем фронтенды `cli` и `ui`, деплой — в последнюю очередь.

---

## 1. Сервер (`packages/core`, Nitro v3, Docker) — «бизнесовая часть»

### 1.1 Auth и устройства
- Better Auth: пользователи, роли (admin/user), пароль + опционально OAuth/2FA.
- **Device-flow:** `harness login https://harness.example.com` → код в браузере → токен устройства. Отзыв в UI, список устройств (ОС, версия, последний онлайн).
- Короткоживущий access-токен для gateway и relay, refresh без разрыва стрима (401 → refresh → retry).

### 1.2 LLM-gateway
- **Провайдеры** (admin): `{type: anthropic|openai-chat|openai-responses, baseUrl, apiKey, headers}`. Ключи зашифрованы мастер-ключом `HARNESS_SECRET`.
- **Каталог моделей:** discovery через `GET /v1/models` плюс ручные записи. Поля: `contextWindow`, `maxOutput`, `capabilities`, цены, видимость по пользователям и ролям. Алиасы и **слоты** (`main`, `small`, `plan`) настраиваются на сервере.
- **Канонический API для клиента:** `POST /api/llm/messages` (стрим), `POST /api/llm/count_tokens`, `GET /api/llm/models`. Запрос — content blocks + tools + system по секциям + effort/thinking + `request-class` (main | subagent | compaction | auxiliary).
- **Трансляция upstream:**
  - `tool_result` → роль `tool`, картинки — в user-сообщение.
  - `thinking` ↔ `reasoning_content` или Responses reasoning.
  - `stop_reason` ↔ `finish_reason`.
  - effort → `output_config.effort` / `reasoning_effort`.
  - reminders → user-контент.
  - `cache_control`-брейкпоинты для Anthropic, стабильный префикс для OpenAI.
- **Capability negotiation** (повтор без отвергнутой фичи, память на модель), ретраи с backoff (`retry-after`, `x-should-retry`), stream idle watchdog и ping до клиента, **fallback-цепочка** (только overloaded/unavailable).
- **Usage и квоты:** нормализованный usage (`input/output/cacheRead/cacheWrite`) → стоимость → лимиты на пользователя и роль, бюджет на сессию. Дашборд usage.
- Роутинг по `request-class`: compaction и auxiliary — на дешёвую модель.
- SSRF-фильтр для upstream baseUrl. Приватные адреса — явным разрешением admin (локальный vLLM/Ollama).

### 1.3 Prompt registry
- Бандл: системный промпт по секциям (идентичность, безопасность, стиль, doing tasks, tool policy, env-шаблон), **описания и схемы всех инструментов**, шаблоны system-reminders, промпт компактора, промпт заголовков, WebFetch-суммаризация, классификатор auto mode, bundled skills (`/init`, `/review`, `/security-review`, `/simplify`), встроенные агенты (`Explore`, `Plan`, `general-purpose`), output styles.
- **Оверрайды под модель или семейство** (у слабых моделей другие формулировки и примеры). Версии, changelog, откат. Позже — A/B по пользователям.
- Клиент получает бандл по ETag на старте и при новых сессиях. **Внутри сессии бандл заморожен** (ради prompt cache), обновление — на новой сессии или после compact. Версия бандла пишется в сессию.
- Редактор в Web UI для admin с предпросмотром собранного промпта под выбранную модель.

### 1.4 Пользовательские данные (источник правды)
| Данные | Как используется клиентом |
|---|---|
| Settings пользователя (модель по умолчанию, effort, режим, output style, attribution, UI) | Кэш, применяется как user-уровень settings |
| Permission-правила user-уровня | Применяются вместе с проектными. «Don't ask again» на user-уровне пишется на сервер |
| User memory (аналог `~/.claude/CLAUDE.md`), auto memory per проект | Инжектится в project context |
| Skills, agents, commands, output styles (личные + общие от admin) | Синк в кэш, загрузка по progressive disclosure |
| Hooks user-уровня | Синк, исполнение локально — только после подтверждения на этой машине (trust) |
| **MCP-серверы — все конфиги и вся авторизация** (см. 1.5) | Подтягиваются на любое устройство. Перетаскивать `.mcp.json` и `~/.claude.json` не нужно |
| Сессии: метаданные + транскрипт | Клиент пишет локальный JSONL и стримит события на сервер. Список сессий, resume и поиск — с сервера на любой машине |
| SSH-хосты (только адреса и алиасы, **без ключей**) | Подсказки в UI |
| Политики admin (managed settings): deny-правила, разрешённые модели, запреты | Высший приоритет |

- **Sync API:** `GET /api/sync?since=<version>` → изменённые документы, `PUT` документа с `baseVersion` (конфликт → 409 → клиент мержит или переспрашивает). Live-обновления через тот же канал, что relay.
- Импорт `~/.claude` (CLAUDE.md, skills, agents, commands, output styles, settings) и CC-сессий: `harness import claude`.

### 1.5 MCP через сервер
Цель: подключил MCP один раз — он работает на всех устройствах, в вебе и на SSH-хостах, без переноса конфигов и повторной авторизации.

- **Реестр MCP на сервере:**
  - Scopes `user` (личные) и `org` (admin раздаёт всем или ролям). Проектный `.mcp.json` из репозитория остаётся как есть, но его одобрение и секреты тоже хранятся на сервере.
  - Поля: имя, транспорт (stdio / Streamable HTTP / SSE / WS), команда или URL, args, env, headers, `alwaysLoad`, enable/disable.
  - Импорт из `~/.claude.json` и `.mcp.json` через `harness import claude`.
- **Авторизация на сервере:**
  - OAuth-флоу проходит через сервер: discovery, callback на `HARNESS_PUBLIC_URL`, refresh до истечения, re-auth при новых scopes.
  - Статические токены, headers и env-секреты шифруются мастер-ключом.
  - Статус «needs auth» виден во всех клиентах. Авторизовался один раз в вебе или desktop — работает везде.
- **Исполнение remote MCP (HTTP/SSE)** — **через сервер.** Клиент вызывает инструменты через сервер, тот подставляет токены и проксирует. Токены никогда не попадают на устройства. Бонус: такие MCP доступны даже сессиям, запущенным из веба.
- **Исполнение stdio MCP** — процесс должен работать локально, рядом с кодом:
  - Конфиг синкается.
  - Секреты (env/args) клиент получает от сервера **в момент запуска** процесса: короткоживущий запрос, только в память, на диск не пишутся, не логируются.
  - Опция `secretsMode: proxy` — для серверов, которым секрет нужен только для исходящих HTTP. Позже.
- **Per-device применимость:**
  - stdio-сервер может требовать `npx`/`uvx`/бинарь, которого на машине нет. Клиент проверяет зависимости и помечает сервер `unavailable on this device` с причиной.
  - Возможны оверрайды под устройство или ОС (другой путь к бинарю), они тоже хранятся на сервере.
- **Безопасность:**
  - Новый stdio-сервер (это исполнение команды на машине) требует **локального подтверждения на каждом устройстве** при первом запуске. Сервер не может молча запустить процесс на машине.
  - Remote MCP с `requiresUserInteraction` всегда спрашивает.
  - Изменения org-MCP видны в UI с diff.
- **Tool search:** список MCP-инструментов и их схемы кэшируются на сервере и раздаются клиентам. `ToolSearch` работает поверх этого кэша, набор замораживается на сессию.
- MCP prompts → slash-команды, resources → `@server:resource` — одинаково во всех клиентах.

### 1.6 Relay, Remote Control, туннели
- **Relay:** клиент открывает исходящее SSE + POST (или WS). Envelope `{seq, sessionId, epoch, uuid, type, payload}`, payload — SDK-протокол.
  - Heartbeat ~20 с, reconnect с jitter, dedup по UUID.
  - **Epoch** вытесняет старое подключение.
  - **FlushGate:** при подключении сначала история, потом live.
  - Батчинг, коды закрытия.
- **Remote control:** по умолчанию включён для всех сессий залогиненного клиента (настраивается). Сессия видна в вебе, управляется оттуда. Permission-промпты и вопросы — в любой клиент, побеждает первый ответ.
- **Spawn:** `harness daemon` регистрирует машину. Из веба: «новая сессия на машине X в папке Y» (только разрешённые корни).
- **Туннели:** preview `https://<id>.preview.<host>` → порт на машине клиента через relay, доступ только владельцу. Терминал в вебе — PTY клиента через relay.
- **Push:** Web Push на permission, вопрос и завершение хода, с подавлением при активности (presence).
- Вложения из веба: upload на сервер → клиент скачивает → `@`-ссылка.

```yaml
# docker-compose.yml
services:
  harness:
    image: harness/server
    ports: ["3000:3000"]
    volumes: ["harness-data:/data"]
    environment:
      HARNESS_PUBLIC_URL: https://harness.example.com
      HARNESS_SECRET: ...
```

---

## 2. Клиент (`packages/client`, Go)

### 2.1 Агентный цикл
- prompt → `gateway.messages` (стрим) → tool_use → permission → **локальное исполнение** → tool_result → повтор.
- Read-only инструменты выполняются параллельно, мутирующие — последовательно. Пара tool_use/tool_result обязательна. `maxTurns`, бюджет (лимиты проверяет и сервер).
- **Очередь и прерывания**, семантика 1:1 из interaction-flows:
  - Enter во время хода ставит сообщение в очередь, оно вливается после tool calls.
  - Ctrl+Enter — «сейчас».
  - Esc отменяет инструмент (kill) и пишет `[Request interrupted by user]`.
  - Немедленные команды (`/model`, `/effort`, `/status`, `/usage`, `/tasks`, `/rename`, `/btw`) идут вне очереди.
- **Сборка контекста:**
  - Prompt bundle (с сервера) + tools.
  - Project context: CLAUDE.md/AGENTS.md из репо + user memory с сервера.
  - Conversation.
  - `DYNAMIC_BOUNDARY`: env машины и git-snapshot идут в первое user-сообщение.
- **Compaction:** сначала обрезка tool outputs, затем суммаризация через gateway (`request-class: compaction`), анти-thrashing.
- **Reminders** по шаблонам из бандла: CLAUDE.md, skills, agents, todo, file-changed, attribution, hooks, output style.
- **Офлайн:** без сервера нет LLM, поэтому нового хода нет. История, `/resume`, просмотр и rewind кода работают из локального кэша. Обрыв посреди хода → ретраи gateway-клиента, затем сессия переходит в «ожидание соединения».

### 2.2 Инструменты (исполняются локально; описания — из prompt bundle)

| Волна | Инструменты |
|---|---|
| **M0** | `Read`, `Write`, `Edit`, `Bash` (cwd переживает вызовы, env — нет; shell-snapshot; 2/10 мин; вывод больше 30K символов → файл + превью; exit 1 для `grep/rg/find/diff/test/git diff` не ошибка), `Glob`, `Grep` |
| **M1** | `AskUserQuestion`, `EnterPlanMode`/`ExitPlanMode`, `TodoWrite`, фоновый Bash + `TaskStop` |
| **M3** | `Agent` (субагенты, `model` — любая модель из каталога сервера), `Skill`, `WebFetch` (fetch локально, суммаризация через gateway), `WebSearch` (через сервер), `ToolSearch`, MCP-инструменты, `NotebookEdit` |
| **M4** | `EnterWorktree`/`ExitWorktree`, `Monitor`, `Cron*`/`ScheduleWakeup`, `SendMessage`/`ListAgents`, `LSP`, `PushNotification`, `SendUserFile` |

### 2.3 Permissions (решает клиент, сервер обойти не может)
- Режимы `default`, `acceptEdits`, `plan`, `dontAsk`, `bypassPermissions`. Позже `auto` (классификатор через gateway).
- Движок правил `Tool(spec)`: deny → ask → allow, разбор составных Bash, снятие обёрток, gitignore-глобы.
- Источники по приоритету: **политики admin (сервер)** → CLI-флаги → `.claude/settings.local.json` → `.claude/settings.json` → **user-правила (сервер)**.
- Read-only allowlist, protected paths, workspace trust (проектные hooks и `.mcp.json` применяются только после подтверждения на этой машине).
- Remote control и spawn из веба не обходят permissions. Режим bypass не включается удалённо.

### 2.4 Сессии
- Локальный JSONL CC-формата (`~/.harness/projects/<slug>/<id>.jsonl`) + стрим событий на сервер. Сервер хранит копию для списка, поиска, веба и resume на другой машине (если тот же репозиторий доступен).
- `--continue`/`--resume`, `/branch`, fork.
- `/rewind`: code+conv / conv / code / summarize. Checkpoints файлов **локально** (`~/.harness/file-history/`) — это данные о ФС машины.
- AI-заголовки через gateway (`request-class: auxiliary`).

### 2.5 Расширяемость
- Memory, skills, agents, commands, output styles, hooks: **user-уровень с сервера**, **проектный уровень из репозитория** (`.claude/`, `.harness/`, CLAUDE.md, AGENTS.md, rules с `paths`, `@import`).
- **Hooks:** `command` исполняются локально, `http` — с клиента.
- **MCP:** конфиги и авторизация — с сервера (раздел 1.5). Remote MCP вызываются через серверный прокси. stdio запускается локально с секретами, выданными в момент запуска, после локального подтверждения на устройстве. Свой `ToolSearch` поверх серверного кэша схем.
- **Plugins/marketplace** — M4, каталог на сервере.

### 2.6 Входы
Бинарь `harness-client` (из `packages/client`) — headless-ядро. Бинарь `harness` (из `packages/cli`) — TUI поверх того же Go-пакета и те же подкоманды.
- `harness login <url>` / `logout` / `whoami`.
- `harness` — TUI в cwd. `harness -p … --output-format stream-json [--input-format stream-json]` (или `harness-client -p …`) — headless и SDK. Через него с клиентом говорит desktop.
- `harness daemon` — машина доступна для spawn из веба.
- `harness ssh user@host:/path` — залить или обновить бинарь, сессия по stdio через SSH. Токен для remote выдаётся отдельный, короткоживущий, привязанный к сессии.
- `harness import claude`, `harness mcp …`, `harness doctor`.

---

## 3. Desktop и mobile (оболочки вокруг `packages/ui`)
- **Desktop:** нативная оболочка (Tauri 2 или Electron) + `packages/ui` с транспортом `ipc`. Go-клиент поставляется как sidecar-бинарь. Логин тот же (device-flow или встроенный браузер).
- **Mobile:** тот же `packages/ui`. Локального исполнения нет, только транспорт `relay`: просмотр и управление сессиями, ответы на permission и вопросы, push.
- **Подпроцесс на сессию:** `harness-client -p --input-format stream-json --output-format stream-json --resume <id>`. Health-check, единый реестр stdio-MCP на хост.
- **SSH:** ключи клиента (`~/.ssh/config`, agent, ProxyJump) → bootstrap бинаря на remote → stdio по SSH-каналу. Remote-раннер ходит в gateway с выданным desktop'ом токеном сессии.
- Нативное: выбор папки, уведомления, трей, deep links, автообновление, open in editor.

## 4. UI (`packages/ui`, Nuxt 4 + Nuxt UI v4) — общий для desktop, mobile и web (дизайн будет предоставлен отдельно)

Каркас — `UDashboardGroup` + `UDashboardSidebar` + resizable `UDashboardPanel`. Чат — `UChatMessages` / `UChatPrompt`, markdown — MDC + Shiki.

- **Сайдбар** (данные с сервера, на любой машине одни и те же):
  - `+ New session`, Pinned/Recents, фильтры по проекту, машине и статусу.
  - Строка: `repo / заголовок`, статус, машина, `+N −M`, PR-бейдж, unread.
- **New session:** где (эта машина / SSH-хост / машина с daemon) → папка (+ worktree) → модель (каталог сервера) → режим.
- **Транскрипт:**
  - Свёрнутые tool calls, Normal/Thinking/Verbose (Ctrl+O), «Run in background».
  - Inline-диффы, кликабельные пути, hover: copy/rewind/fork.
- **Карточки:**
  - permission («Yes / Yes, don't ask again for `<pattern>` / No» + комментарий, «2 of 5»);
  - AskUserQuestion;
  - план;
  - лимит, квота или ретрай;
  - машина офлайн;
  - межсессионное сообщение.
- **Pinned task list. Prompt:**
  - Очередь с ✕, Stop.
  - `@`-файлы, `/`-меню, вложения.
  - Пикеры Model/Effort/Mode, кольцо контекста, usage и квота.
- **Панели:** Tasks, Diff (комментарии, Review, Create PR), Plan, Subagent, Terminal (PTY открывает Go-клиент; desktop — через ipc, web/mobile — через relay), File, Preview (туннель), split view, side chat, status footer.
- **Аккаунт (пользователь):** профиль, устройства и токены, settings, memory, skills, agents, commands, output styles, MCP-коннекторы, usage.
- **Admin:** пользователи и роли, провайдеры и ключи, каталог моделей и слоты, **prompt registry** (редактор, версии, оверрайды под модель, предпросмотр), политики, квоты, usage по пользователям.

---

## 5. Дорожная карта

Порядок: **ядро (`core` + `client`) → фронтенды (`cli`, `ui`) → деплой.** До фронтендов всё проверяется тестами и headless-режимом `harness-client -p`.

### M0 — Скелет core + client (≈3 нед.)
1. `docs/protocol/`: gateway API, формат событий стрима, SDK-протокол (stdio NDJSON), auth/device-flow.
2. **core (Nitro v3):** Better Auth (admin + пользователи), device-flow, провайдеры с шифрованием ключей, каталог моделей, gateway `POST /api/llm/messages` (SSE) с адаптерами Anthropic и OpenAI-chat. **Контрактные тесты на фикстурах** (Anthropic, OpenAI, DeepSeek, OpenRouter, vLLM/Ollama). Prompt registry v0: бандл из файлов, отдача по ETag.
3. **client (Go):** `login` (device-flow), загрузка prompt bundle, агентный цикл через gateway, `Read/Write/Edit/Bash/Glob/Grep`, простые permissions, локальный JSONL, headless `-p` со stream-json in/out.

**Готово, когда** core запущен локально (`pnpm dev`), admin добавил ключи Claude и любого OpenAI-совместимого провайдера, а `harness-client -p "почини тест" --output-format stream-json` в папке проекта чинит тест на обеих моделях. Ключей на машине при этом нет.

### M1 — Паритет ядра + sync (≈4 нед.)
- client: permissions-движок, режимы, trust, plan mode, AskUserQuestion, TodoWrite, очередь и прерывания (через control-запросы), фоновый Bash.
- client: сборка контекста, reminders, compaction, checkpoints/rewind, resume/fork.
- core: слоты, effort, capability negotiation, fallback, usage и квоты.
- core + client: **sync API** (settings, user memory, user-правила, сессии), `import claude`.

### M2 — Расширяемость + relay (≈4 нед.)
- Skills, agents/subagents, commands, output styles (сервер + репо), hooks, WebFetch/WebSearch.
- **MCP через сервер:** реестр, OAuth и секреты, прокси для remote, выдача секретов для stdio, per-device подтверждение, кэш схем + ToolSearch.
- Relay и remote control в core и client: зеркало сессий, ответы на permission из любого подключения, spawn через `daemon`, туннели (preview, PTY).

### M3 — Фронтенды
- **cli (Go, тулчейн charm.land):** TUI 1:1 с Claude Code. Перед разработкой — визуальный ресёрч: живые сессии `claude` в терминале, снимки всех состояний (стрим, tool calls, permission-промпты, очередь, `/`-меню, статус-строка, диффы, спиннеры), truecolor и ANSI-fallback.
- **ui (Nuxt UI):** по присланному дизайну. Сайдбар, транскрипт, карточки, prompt, панели. Desktop-оболочка с sidecar-клиентом, mobile.
- Admin-экраны: провайдеры, модели, prompt registry, пользователи, политики, usage.

### M4 — Деплой и продвинутое
- Docker-образ core, compose, бэкапы, обновления клиентов (self-update бинарей, авто-обновление на SSH-хостах).
- Git-панели и PR/CI, worktrees, sandbox-runtime, auto mode, Monitor/Cron, `SendMessage`, auto memory, plugins/marketplace, LSP, A/B промптов.

---

## 6. Риски

| Риск | Митигация |
|---|---|
| Сервер — единая точка отказа (без него нет LLM) | Простой деплой и бэкапы volume. Клиент переживает короткие обрывы (ретраи, ожидание соединения). Локальная история доступна офлайн |
| Латентность и стрим через gateway | Gateway — тонкий стрим-прокси без буферизации, ping, сервер рядом с провайдерами |
| Компрометация сервера → управление машинами | Permissions решает клиент. Bypass удалённо не включается. Spawn — только в разрешённых корнях. Hooks и MCP с сервера требуют локального trust. Отзыв устройств |
| Обновление промптов ломает поведение | Версии, бандл заморожен в сессии, откат, предпросмотр, eval-набор задач перед публикацией |
| Зоопарк «OpenAI-совместимых» эндпоинтов | Capability-профили на сервере, контрактные тесты, negotiation, оверрайды промптов под модель |
| Секреты stdio-MCP на устройстве (процессу они нужны в открытом виде) | Выдача только в момент запуска, только в память, не на диск. Отзыв устройства → отзыв выдачи. Где возможно — remote MCP через серверный прокси вместо stdio |
| Конфликты синка (правка с двух машин) | Версии документов, 409 + merge, для memory — последняя запись с историей версий |
| Prompt cache ломается → дорого | Бандл заморожен на сессию, стабильный префикс, deferred MCP, метрика cache hit |
| Юридический | Clean-room, свои промпты, совместимость только форматов и протоколов |

## 7. Открытые вопросы (выбраны дефолты)

1. ~~Локальный провайдер в обход сервера~~ — **решено: нет, все LLM-вызовы только через gateway сервера.** Клиент не умеет говорить с провайдерами напрямую. Локальные модели (vLLM/Ollama) подключаются как провайдер на сервере: они должны быть доступны из сети сервера, admin явно разрешает приватные адреса.
2. Хранить полные транскрипты на сервере всегда (дефолт) или только метаданные + опционально контент.
3. Оболочка desktop + mobile: **Tauri 2** (дефолт: одна оболочка на desktop и iOS/Android, Go-клиент как sidecar) или Electron + Capacitor.
4. OpenAI Responses API в M0 или позже.
5. Бэкенд WebSearch на сервере (Brave, Tavily, SearXNG, нативный у провайдера).

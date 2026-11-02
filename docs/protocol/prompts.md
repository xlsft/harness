# Prompt bundle

Промпты живут на сервере (prompt registry). Клиент собирает из них запрос к модели. Своих текстов промптов у клиента нет, кроме аварийного минимального fallback на случай первого запуска без сети.

## `GET /api/prompts/bundle?model=<model_id>`
- Требует `Authorization`.
- Поддерживает `If-None-Match`: ответ `304`, если бандл не менялся.
- Ответ содержит `ETag` с версией.
- Сервер уже применил оверрайды под модель: клиент получает готовые тексты для конкретной модели.

```json
{
  "version": "2026-11-02.3",
  "model": "claude-sonnet",
  "system": [
    { "id": "identity", "text": "…" },
    { "id": "safety", "text": "…" },
    { "id": "style", "text": "…" },
    { "id": "doing_tasks", "text": "…" },
    { "id": "tool_policy", "text": "…" },
    { "id": "__dynamic_boundary__" },
    { "id": "env", "text": "Working directory: {{cwd}}\nPlatform: {{platform}}\nShell: {{shell}}\nOS Version: {{os_version}}\nToday's date: {{date}}\nIs a git repository: {{is_git}}\nModel: {{model_display_name}} ({{model_id}})" }
  ],
  "tools": {
    "Read":  { "description": "…", "input_schema": { … } },
    "Write": { "description": "…", "input_schema": { … } },
    "Edit":  { "description": "…", "input_schema": { … } },
    "Bash":  { "description": "…", "input_schema": { … } },
    "Glob":  { "description": "…", "input_schema": { … } },
    "Grep":  { "description": "…", "input_schema": { … } }
  },
  "reminders": {
    "project_instructions": "<system-reminder>\nAs you answer the user's questions, you can use the following context:\n{{content}}\n</system-reminder>",
    "file_changed": "…{{path}}…",
    "empty_file": "…",
    "interrupted": "[Request interrupted by user]"
  },
  "compaction": { "instruction": "…" },
  "title": { "instruction": "…" }
}
```

## Правила

- Секции `system` до `__dynamic_boundary__` — статика: клиент кладёт её в `system` с `cache: true` на последнем блоке. Секции после маркера — динамика, клиент подставляет плейсхолдеры.
- Плейсхолдеры: `{{name}}`. Неизвестный плейсхолдер клиент оставляет пустым и пишет предупреждение в лог.
- `tools` задаёт описание и схему каждого инструмента. **Семантику исполнения** реализует клиент, она описана в `docs/PLAN.md` §2.2 и должна совпадать с текстом описания. Инструменты, которых нет в бандле, клиент модели не отдаёт.
- **Внутри сессии бандл заморожен.** Клиент запоминает `version` при старте сессии и использует ту же версию до конца сессии или до compaction. Иначе ломается prompt cache.
- Кэш на клиенте: `~/.harness/cache/prompts/<model>/<version>.json`. Если сервер недоступен, берётся последний закэшированный бандл. Нового хода без сервера всё равно нет: LLM тоже за сервером.
- M0: сервер отдаёт бандл, собранный из файлов `packages/core/prompts/**`. Редактор и версии в БД появятся в M2.
- Тексты промптов пишем сами (clean-room). Дословно из Claude Code не копируем.

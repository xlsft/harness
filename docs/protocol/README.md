# Протоколы harness

Контракты между пакетами. TS (`packages/core`) и Go (`packages/client`, `packages/cli`) общего кода не имеют. Источник правды — эти документы. Если поведение и текст расходятся, исправляется один из них в том же PR.

| Документ | Стороны | Статус |
|---|---|---|
| [auth.md](auth.md) | client ↔ core | M0 |
| [gateway.md](gateway.md) | client ↔ core | M0 |
| [prompts.md](prompts.md) | client ↔ core | M0 |
| [sdk-stream.md](sdk-stream.md) | frontend (cli/ui/desktop) ↔ client, stdio NDJSON | M0 (пишет client) |
| sync.md | client ↔ core | M1 |
| relay.md | client ↔ core ↔ web/mobile | M2 |

## Общие правила

- Транспорт: HTTPS, JSON (UTF-8), `snake_case` в полях протокола.
- Версия: заголовок `Harness-Protocol: 1` в каждом запросе клиента. Сервер отвечает `426` с `{error:{type:"protocol_version", min, max}}`, если версия не поддерживается.
- Идентификация клиента: `User-Agent: harness-client/<version> (<os>; <arch>)`.
- Ошибки — всегда JSON:
  ```json
  { "error": { "type": "invalid_request | authentication | permission | not_found | rate_limit | quota_exceeded | overloaded | upstream | internal", "message": "…", "retry_after_ms": 1200 } }
  ```
  HTTP-коды: 400, 401, 403, 404, 429, 529 (overloaded), 502 (upstream), 500.
- Время — RFC 3339 UTC. ID — непрозрачные строки с префиксом (`usr_`, `dev_`, `prv_`, `ses_`, `msg_`).

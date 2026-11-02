# Auth: device-flow и токены

Веб-логин — обычная сессия Better Auth (cookie). Клиенты (CLI, desktop, SSH-раннеры) получают **токен устройства** через device-flow по образцу RFC 8628.

## Device-flow

### 1. `POST /api/auth/device/code`
```json
// запрос
{ "client_name": "harness-client", "device_name": "macbook-pro", "os": "darwin", "arch": "arm64" }
// ответ 200
{ "device_code": "…", "user_code": "WDJB-MJHT", "verification_uri": "https://host/device",
  "verification_uri_complete": "https://host/device?code=WDJB-MJHT", "expires_in": 600, "interval": 5 }
```

### 2. Пользователь открывает `verification_uri` в браузере
Логинится, видит имя устройства и подтверждает.

### 3. `POST /api/auth/device/token`, клиент опрашивает каждые `interval` секунд
```json
// запрос
{ "device_code": "…" }
// ответ 400, пока не подтверждено
{ "error": { "type": "authorization_pending" } }   // также: "slow_down" (+5 с к интервалу), "expired_token", "access_denied"
// ответ 200
{ "device_id": "dev_…", "device_token": "hdt_…", "access_token": "hat_…", "access_expires_in": 3600,
  "user": { "id": "usr_…", "email": "…", "name": "…", "role": "admin | user" } }
```

## Токены

| Токен | Назначение | Срок | Хранение на клиенте |
|---|---|---|---|
| `device_token` (`hdt_…`) | Только обмен на access-токен | До отзыва | OS keychain, fallback `~/.harness/credentials.json` (0600) |
| `access_token` (`hat_…`) | Все API-запросы: `Authorization: Bearer hat_…` | 1 час | В памяти |

### `POST /api/auth/device/refresh`
```json
// запрос
{ "device_token": "hdt_…" }
// ответ 200
{ "access_token": "hat_…", "access_expires_in": 3600 }
// ответ 401 → устройство отозвано, клиент удаляет credentials и просит `harness login`
```

Правила для клиента:
- Обновлять токен заранее, за 5 минут до истечения.
- На любой `401` делать один refresh и повторять запрос. Активный SSE-стрим при этом не рвётся.

## Прочее
- `GET /api/auth/me` → `{ user, device: { id, name, created_at, last_seen_at } }`.
- `POST /api/auth/device/logout` — отзыв текущего устройства.
- Управление устройствами (список, отзыв) — в веб-UI. API для него будет описан позже.

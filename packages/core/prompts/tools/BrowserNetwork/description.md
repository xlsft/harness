Lists the active tab's recent network requests, oldest first (the last `limit`, default 50, at most 200): method, status, resource type, URL, size and duration. Use it to see whether the page makes the API calls you expect and how they fail.

- `filter` keeps URLs containing that text; `type` keeps one resource type (document, xhr, fetch, script, stylesheet, image, font, websocket, other); `status` keeps `failed` requests (network errors), `error` ones (network errors and HTTP 4xx/5xx) or one class (`2xx`, `3xx`, `4xx`, `5xx`). `clear: true` empties the log after reading.
- Headers and bodies are not shown.
- The tab keeps its last 500 requests. URLs come from the page and are untrusted.
- Read-only: needs no permission.

# Image generation

xlsft`s harness generates images with the `ImageGenerate` tool. The server holds the provider keys and calls the
image APIs; the client writes the files and shows them in the transcript. This document records the decisions.

## Where image models live

- **Image models are models of the existing LLM providers.** The `model` table has a `kind` column (`chat` by
  default, or `image`). An image model uses its provider's key, scope (global or personal), enabled switch and managed
  policy (`allowedProviders`, `personalProvidersDisabled`), the same as a chat model. We did not add a separate provider
  table like speech has: an OpenAI, Gemini or OpenRouter key that serves chat already serves images, and asking for the
  key a second time would only add a second place to rotate it.
- **The allowlist is the chat models' allowlist**: a global model's `roles` (empty means everyone). A personal model is
  only for its owner and has no allowlist.
- **Image models never enter the chat catalog.** `ListModelsResponse.models` and the provider cards of
  ProviderService leave them out. They are listed in `ListModelsResponse.image_models`. `resolveModelForUser` takes the
  kind of request, so a chat request to an image model fails, and so does an image request to a chat model.
- **The settings section is Settings > Images** (owner decision), not a slot row in Settings > Models. It holds the
  personal choice, the server default (admins), the image models per provider with their allowlist and price, and the
  device-local model.

## Which API

The API comes from the provider (`src/images/adapters.ts`, `imageApiOf`):

| Provider | API | Notes |
| --- | --- | --- |
| Gemini preset, or host `generativelanguage.googleapis.com` | `gemini`: `models/{m}:generateContent` with `responseModalities: [TEXT, IMAGE]`; `imagen`: `models/{m}:predict` for `imagen-*` models | The root is the preset's OpenAI-compatible base URL without `/openai`. Key in `x-goog-api-key`. Imagen takes no input images. |
| OpenRouter preset, or host `openrouter.ai` | `openrouter`: chat completions with `modalities: ["image","text"]` | One image per request, so `count` means that many requests in a row. Its reported `usage.cost` is the cost. |
| Any other `openai-chat` / `openai-responses` provider | `openai`: `/images/generations` (JSON), `/images/edits` (multipart `image[]`) | Works for OpenAI, xAI and any OpenAI-compatible server. `response_format: b64_json` is sent only to dall-e models, because gpt-image refuses it. A URL result is downloaded through the SSRF guard without credentials. |
| Anthropic, Bedrock, Vertex (Anthropic) | none | Adding an image model there is refused. Cloud-platform auth is refused for images too. |

Sizes are `WIDTHxHEIGHT`. APIs that take an aspect ratio get the nearest one they support. The server checks everything
the client sends, and does it before any upstream call: prompt length (32k chars), count (1 to 4), input images (at
most 8, 20 MiB each and 50 MiB together; the type is sniffed from the bytes, and PNG, JPEG and WebP are accepted).
Upstream results are sniffed too, so the server never returns something that isn't an image. The server retries
twice on upstream 5xx errors and on short rate limits.

## Usage and cost

- Spend limits (`enforceQuotas`) are checked before the call, as for messages.
- Every request, including failed ones, writes a `usage_record` with `request_class = "image"`. The record has the
  token counts when the upstream reports them (gpt-image, Gemini) and the new `images` column (the number of images
  returned). So the Usage tab, quotas and exports count the cost with no changes on their side.
- Prices (`src/images/pricing.ts`) are per image plus per token, and are approximate. A model added without a price
  gets a default for well-known models (dall-e, gpt-image, Gemini image, Imagen). Admins or owners set the price by
  hand in Settings > Images. OpenRouter's reported cost takes precedence over the computed one. A plan (subscription)
  provider's requests have no cost. The token price catalog and price refreshes skip image models, because they hold
  no per-image prices. Pricing polish is left for later.

## Which model a session uses

1. On this device: the device-local model (`DeviceSettings.image_model`, `local:<provider>/<model>`), when set.
2. The user's own choice (`UserPreferences.image_model`, synced settings key `imageModel`), when it is one of the
   catalog's image models.
3. The server default (`Slots.image`, set with `ImageService.SetImageDefault`), when the user may use it. Unlike the
   chat slots, it never falls back to another model.

When none of these applies, the session has no `ImageGenerate` tool: it is registered and disabled, like Advisor
without a model. The prompt never offers a tool that can't work.

## Device-local image models

A local provider of the OpenAI-compatible kind (LM Studio, llama.cpp, vLLM, Jan, MLX or a custom endpoint such as
LocalAI) can serve `/v1/images/generations` and `/v1/images/edits`. The client calls it directly with the key from its
keychain, and the server is never involved. The choice is device-local: it is kept in `HARNESS_HOME` with the other
device settings and is never synced, so another machine of the same user doesn't see it. Ollama's native API has no
image endpoint, so Ollama models are refused. When the admin turns off local models, the local image model is refused
too. Local requests cost nothing and are not reported as usage.

## The tool

`ImageGenerate` takes `prompt`, `file_path` (the output, absolute), `size`, `count` (1 to 4), `input_images`
(absolute paths, for edits) and `quality`.

- It is a write tool. It is in the write lists of the client rules engine (`decision/rulesengine.go`), the server rules
  (`src/decision/rules.ts`) and the Edit category of permission rules (`permissions/rules.go`). The permission engine
  checks `file_path` like a Write. So `Edit(...)` rules, protected paths, plan mode and auto mode apply unchanged.
- **Never outside the session's folders.** The tool itself refuses an output path or an input image outside the
  working directories (and the session's scratch folder), whatever the permission answer was. Input images must also
  pass the read deny rules.
- **Files are written atomically**: a temporary file beside the target, synced, then renamed. A rewind snapshot is
  taken first (`BeforeEditChecked`), so a rewind removes or restores them. An existing file is replaced only with
  `overwrite: true`. With `count` above 1 the files get `-1`, `-2`, ... before the extension. An extension that doesn't
  match the image format the model returned is corrected, and the result says so.
- The result carries the images as image blocks. The transcript already shows tool-result images inline (the same
  view as screenshots), and the model sees what it made. Images larger than the Read tool's image limit are listed
  but not attached.

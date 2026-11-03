/** Connect implementation of harness.speech.v1.SpeechService: proto <-> src/speech/service.ts. */
import type { ConnectRouter, HandlerContext } from "@connectrpc/connect";
import { SpeechKind, SpeechLocation, SpeechService, type SynthesizeResponse } from "../gen/harness/speech/v1/speech_pb.ts";
import type { CatalogVoice } from "../speech/catalog.ts";
import {
  clearSpeechProviderKey,
  listSpeechProviders,
  listSpeechVoices,
  setSpeechProviderKey,
  synthesizeForUser,
  transcribeForUser,
  type AudioChunk,
  type Kind,
  type ProviderView,
} from "../speech/service.ts";
import { requirePrincipal } from "./context.ts";
import { guard, toConnectError } from "./errors.ts";

const kindOf = (k: SpeechKind): Kind | undefined => (k === SpeechKind.STT ? "stt" : k === SpeechKind.TTS ? "tts" : undefined);

function voiceToProto(v: CatalogVoice) {
  return { id: v.id, displayName: v.display_name, language: v.language ?? "", gender: v.gender ?? "", description: v.description ?? "", previewUrl: v.preview_url ?? "" };
}

function providerToProto(p: ProviderView) {
  return {
    id: p.id,
    displayName: p.display_name,
    kind: p.kind === "stt" ? SpeechKind.STT : SpeechKind.TTS,
    location: SpeechLocation.GLOBAL,
    configured: p.configured,
    available: p.configured,
    unavailableReason: p.configured ? "" : "Not configured on the server: an admin needs to set its API key",
    models: p.models.map((m) => ({ id: m.id, displayName: m.display_name, description: m.description ?? "", voiceIds: m.voice_ids ?? [], languages: m.languages ?? [] })),
    voices: p.voices.map(voiceToProto),
    listsVoices: p.lists_voices,
    defaultModel: p.default_model,
    defaultVoice: p.default_voice,
    supportsSpeed: p.supports_speed,
    supportsLanguage: p.supports_language,
    inputMimeTypes: p.input_mime_types,
    apiKeySet: p.api_key_set,
    apiKeyHint: p.api_key_hint,
    baseUrl: p.base_url,
    docsUrl: p.docs_url,
    keyUrl: p.key_url,
    icon: p.icon,
    description: p.description,
  };
}

export function registerSpeechService(router: ConnectRouter): void {
  router.service(SpeechService, {
    listProviders: guard(async (req, ctx: HandlerContext) => {
      const { user } = await requirePrincipal(ctx);
      const r = await listSpeechProviders(user, kindOf(req.kind));
      return { providers: r.providers.map(providerToProto), canManage: r.can_manage };
    }),
    setProviderKey: guard(async (req, ctx: HandlerContext) => {
      const { user } = await requirePrincipal(ctx);
      return { providers: (await setSpeechProviderKey(user, req.providerId, req.apiKey, req.baseUrl)).map(providerToProto) };
    }),
    clearProviderKey: guard(async (req, ctx: HandlerContext) => {
      const { user } = await requirePrincipal(ctx);
      await clearSpeechProviderKey(user, req.providerId);
      return {};
    }),
    listVoices: guard(async (req, ctx: HandlerContext) => {
      const { user } = await requirePrincipal(ctx);
      return { voices: (await listSpeechVoices(user, req.providerId, req.model || undefined, ctx.signal)).map(voiceToProto) };
    }),
    transcribe: guard(async (req, ctx: HandlerContext) => {
      const { user } = await requirePrincipal(ctx);
      const r = await transcribeForUser(user, {
        providerId: req.providerId,
        model: req.model,
        audio: req.audio,
        mimeType: req.mimeType,
        language: req.language,
        prompt: req.prompt,
        signal: ctx.signal,
      });
      return { text: r.text, language: r.language ?? "", durationSeconds: r.durationSeconds ?? 0, providerId: r.providerId, model: r.model };
    }),
    async *synthesize(req, ctx: HandlerContext): AsyncGenerator<SynthesizeResponse> {
      let chunks: AsyncGenerator<AudioChunk>;
      try {
        const { user } = await requirePrincipal(ctx);
        chunks = await synthesizeForUser(user, {
          providerId: req.providerId,
          model: req.model,
          voice: req.voice,
          text: req.text,
          speed: req.speed,
          language: req.language,
          signal: ctx.signal,
        });
      } catch (err) {
        throw toConnectError(err);
      }
      try {
        for await (const c of chunks) {
          yield { $typeName: "harness.speech.v1.SynthesizeResponse", audio: c.audio, mimeType: c.mimeType ?? "", done: c.done ?? false, characters: BigInt(c.characters ?? 0) };
        }
      } catch (err) {
        throw toConnectError(err);
      }
    },
  });
}

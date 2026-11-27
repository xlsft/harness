/**
 * What every chat app provider implements (docs/design/chat.md). Slack is in slack.ts, Telegram in telegram.ts; a Teams
 * provider implements the same interface and gets its own route, and service.ts stays as it is.
 */

/** Where a message lives: a conversation and the thread in it. */
export interface ChatPlace {
  teamId: string;
  channel: string;
  /** The thread's root message id. */
  threadId: string;
}

/** A button on a message. `value` comes back in a ChatAction; providers keep it opaque. */
export interface ChatButton {
  id: string;
  label: string;
  value: string;
  style?: "primary" | "danger";
}

export interface ChatOutbound {
  text: string;
  buttons?: ChatButton[];
}

export type ChatInbound =
  /** The provider checks the request URL (Slack url_verification): answer with this body. */
  | { kind: "challenge"; challenge: string }
  /** Someone wrote: a mention, a DM, or a reply in a thread. */
  | {
      kind: "message";
      /** Dedup key of the message itself (Slack: channel and ts), so two events for one message do one thing. */
      messageKey: string;
      place: ChatPlace;
      /** This message's own id (Slack ts); equal to place.threadId for a thread's first message. */
      messageId: string;
      userId: string;
      text: string;
      /** A mention of the bot or a direct message: may start a session. A plain thread reply only steers. */
      addressed: boolean;
      /** The sender's name when the request carries it (Telegram); otherwise the service asks profile(). */
      userName?: string;
      /** A command the provider recognized: "help" answers with what the bot does, "new" starts a new session even
       *  where one is going on (chat threading). */
      command?: "help" | "new";
    }
  /** Someone pressed a button. */
  | {
      kind: "action";
      place: ChatPlace;
      messageId: string;
      userId: string;
      value: string;
      /** The press to acknowledge (Telegram callback_query id); see ChatProviderClient.answerAction. */
      ackId?: string;
    }
  /** Nothing to do (other event types, the bot's own messages, edits). */
  | { kind: "ignore" };

export interface ChatVerifyResult {
  ok: boolean;
  /** HTTP status to answer with when not ok. */
  status?: number;
  reason?: string;
  /** Replay protection key (a hash of the signature): stored, and refused when seen again. */
  replayKey?: string;
  /** Provider retry of an already delivered event (handled once): the provider's own delivery id. */
  deliveryId?: string;
}

export interface ChatProfile {
  name: string;
  /** "" when the provider does not share it. */
  email: string;
  bot: boolean;
}

export interface ChatWhoAmI {
  teamId: string;
  teamName: string;
  botUserId: string;
}

/** The provider side of a connected app (credentials bound). */
export interface ChatProviderClient {
  whoami(): Promise<ChatWhoAmI>;
  post(place: ChatPlace, msg: ChatOutbound): Promise<string>;
  update(place: ChatPlace, messageId: string, msg: ChatOutbound): Promise<void>;
  /** A message only `userId` sees. */
  ephemeral(place: ChatPlace, userId: string, text: string): Promise<void>;
  profile(userId: string): Promise<ChatProfile>;
  /** Acknowledges a button press, with a short notice only the presser sees. Providers that need no acknowledgement
   *  (Slack) leave it out, and the service uses ephemeral() for the notice. */
  answerAction?(ackId: string, text?: string): Promise<void>;
}

export interface ChatProvider {
  /** harness.chat.v1.ChatProvider. */
  readonly id: number;
  readonly name: string;
  /**
   * How messages map to sessions. "thread": each thread is one session (Slack). "chat": a conversation has no threads,
   * so the latest session started in it is the one later messages steer, until a "new" command starts another
   * (Telegram private chats).
   */
  readonly threading: "thread" | "chat";
  /** How a person starts a session, for the bot's own messages ("Mention me", "Write to me"). */
  readonly startHint: string;
  /** Signature, timestamp and replay key of an inbound request, with the stored signing secret. */
  verify(headers: Headers, raw: string, signingSecret: string, now?: number): ChatVerifyResult;
  /** The inbound request as items (after verify). */
  parse(headers: Headers, raw: string, botUserId: string): ChatInbound[];
  client(secrets: Record<string, string>): ChatProviderClient;
}

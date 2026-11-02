/**
 * Fan-out of relay traffic between RPC handlers: live session events to viewers, commands and supersede
 * notices to host streams, and command acks to held calls.
 *
 * The default implementation is in-process (one core replica). A multi-replica deployment replaces it with
 * a pub/sub backend (Postgres LISTEN/NOTIFY, Redis, NATS) behind the same interface; everything durable
 * already lives in the database, so the bus only carries notifications.
 */
import type { SessionEvent } from "../gen/harness/agent/v1/agent_pb.ts";
import type { Command, CommandAck, SessionSuperseded } from "../gen/harness/relay/v1/relay_pb.ts";

/** Items fanned out to SubscribeSession streams of one session. */
export type SessionFeedItem =
  /** A durable event (in seq order, without gaps) or an ephemeral AssistantDelta. */
  | { kind: "event"; event: SessionEvent }
  /** The owning host, its online state or its epoch changed. */
  | { kind: "host" }
  /** The set of viewers or their focus changed. */
  | { kind: "presence" };

/** Items delivered to the OpenHostStream of one host. */
export type HostFeedItem =
  | { kind: "command"; command: Command }
  | { kind: "superseded"; superseded: SessionSuperseded }
  /** A newer OpenHostStream for the same host id replaces this one. */
  | { kind: "replaced"; streamId: string };

export interface RelayBus {
  publishSession(sessionId: string, item: SessionFeedItem): void;
  subscribeSession(sessionId: string, fn: (item: SessionFeedItem) => void): () => void;
  publishHost(hostId: string, item: HostFeedItem): void;
  subscribeHost(hostId: string, fn: (item: HostFeedItem) => void): () => void;
  publishAck(commandId: string, ack: CommandAck): void;
  subscribeAck(commandId: string, fn: (ack: CommandAck) => void): () => void;
}

class Topics<T> {
  private readonly subs = new Map<string, Set<(item: T) => void>>();

  publish(key: string, item: T): void {
    const set = this.subs.get(key);
    if (!set) return;
    for (const fn of [...set]) {
      try {
        fn(item);
      } catch (err) {
        console.error("[harness] relay subscriber failed", err);
      }
    }
  }

  subscribe(key: string, fn: (item: T) => void): () => void {
    let set = this.subs.get(key);
    if (!set) this.subs.set(key, (set = new Set()));
    set.add(fn);
    return () => {
      set.delete(fn);
      if (!set.size && this.subs.get(key) === set) this.subs.delete(key);
    };
  }
}

/** Synchronous in-process bus: publish returns after every subscriber ran. */
export class InProcessBus implements RelayBus {
  private readonly sessions = new Topics<SessionFeedItem>();
  private readonly hosts = new Topics<HostFeedItem>();
  private readonly acks = new Topics<CommandAck>();

  publishSession(sessionId: string, item: SessionFeedItem): void {
    this.sessions.publish(sessionId, item);
  }
  subscribeSession(sessionId: string, fn: (item: SessionFeedItem) => void): () => void {
    return this.sessions.subscribe(sessionId, fn);
  }
  publishHost(hostId: string, item: HostFeedItem): void {
    this.hosts.publish(hostId, item);
  }
  subscribeHost(hostId: string, fn: (item: HostFeedItem) => void): () => void {
    return this.hosts.subscribe(hostId, fn);
  }
  publishAck(commandId: string, ack: CommandAck): void {
    this.acks.publish(commandId, ack);
  }
  subscribeAck(commandId: string, fn: (ack: CommandAck) => void): () => void {
    return this.acks.subscribe(commandId, fn);
  }
}

let bus: RelayBus = new InProcessBus();

export function relayBus(): RelayBus {
  return bus;
}

/** Replaces the bus (multi-replica backends, tests). */
export function setRelayBus(b: RelayBus): void {
  bus = b;
}

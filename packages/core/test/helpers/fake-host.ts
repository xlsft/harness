/** A fake machine on the relay for chat app tests (chat.test.ts, telegram.test.ts). */
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { randomUUID } from "node:crypto";
import { SessionEventSchema, SessionStatus } from "../../src/gen/harness/agent/v1/agent_pb.ts";
import { CommandAck_Outcome, type Command, type CommandAck } from "../../src/gen/harness/relay/v1/relay_pb.ts";
import type { TestServer } from "./server.ts";

type Clients = ReturnType<TestServer["as"]>;

/** A fake machine on the relay: answers folder queries, spawns, and records every other command. */
export class FakeHost {
  hostId = "";
  commands: Command[] = [];
  sessionId = "";
  epoch = 0n;
  seq = 0;
  private ctl = new AbortController();
  constructor(
    private readonly c: Clients,
    private readonly folders: string[],
  ) {}

  async start(remoteStarts = true) {
    this.hostId = (await this.c.relay.registerHost({ instanceId: `inst-${randomUUID()}`, clientVersion: "test", remoteStarts })).hostId;
    void (async () => {
      try {
        for await (const r of this.c.relay.openHostStream({ hostId: this.hostId }, { signal: this.ctl.signal })) {
          if (r.event.case === "command") await this.onCommand(r.event.value);
        }
      } catch {
        // closed
      }
    })();
  }

  private async ack(a: Partial<CommandAck> & { commandId: string }) {
    await this.c.relay.ackCommands({ hostId: this.hostId, acks: [{ outcome: CommandAck_Outcome.OK, ...a } as never] });
  }

  private async onCommand(cmd: Command) {
    this.commands.push(cmd);
    switch (cmd.payload.case) {
      case "listRecentFolders":
        return this.ack({ commandId: cmd.commandId, result: { case: "listRecentFolders", value: { folders: this.folders.map((path) => ({ path, name: path, exists: true })) } } as never });
      case "spawnSession": {
        const id = `ses-${randomUUID()}`;
        const r = await this.c.relay.registerSession({ hostId: this.hostId, session: { id, cwd: cmd.payload.value.create?.cwd ?? "", title: "t", status: SessionStatus.IDLE } });
        this.sessionId = id;
        this.epoch = r.epoch;
        return this.ack({ commandId: cmd.commandId, result: { case: "spawnSession", value: { session: { id, cwd: cmd.payload.value.create?.cwd } } } as never });
      }
      case "respondToPermission":
        return this.ack({ commandId: cmd.commandId });
      case "sendUserMessage":
        return this.ack({ commandId: cmd.commandId });
      default:
        return this.ack({ commandId: cmd.commandId });
    }
  }

  async publish(...events: MessageInitShape<typeof SessionEventSchema>["event"][]) {
    await this.c.relay.publishEvents({
      hostId: this.hostId,
      sessionId: this.sessionId,
      epoch: this.epoch,
      events: events.map((event) => {
        const seq = ++this.seq;
        return { uuid: `${this.sessionId}-${seq}`, event: create(SessionEventSchema, { seq: BigInt(seq), sessionId: this.sessionId, event }) };
      }),
    });
  }

  stop() {
    this.ctl.abort();
  }
}

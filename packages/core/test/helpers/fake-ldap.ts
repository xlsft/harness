/**
 * In-process fake LDAP server for tests: plain ldap:// on 127.0.0.1, simple bind and search (base / sub)
 * over an in-memory directory, decoded and encoded with ldapts' own BER code. Records every search filter
 * (as the server received it) so tests can check what a login turned into.
 */
import { createServer, type Server, type Socket } from "node:net";
import {
  AndFilter,
  BerReader,
  BerWriter,
  BindRequest,
  EqualityFilter,
  NotFilter,
  OrFilter,
  PresenceFilter,
  SearchRequest,
  SubstringFilter,
  type Filter,
} from "ldapts";

export interface FakeEntry {
  dn: string;
  attributes: Record<string, string | string[]>;
  /** Simple bind password; entries without one cannot bind. */
  password?: string;
}

const OP = { bind: 0x60, bindRes: 0x61, unbind: 0x42, search: 0x63, entry: 0x64, done: 0x65 } as const;
const RESULT = { success: 0, noSuchObject: 32, invalidCredentials: 49, unwilling: 53 } as const;

const norm = (dn: string) => dn.toLowerCase().replace(/\s*([,=])\s*/g, "$1");

export class FakeLdap {
  entries: FakeEntry[] = [];
  /** Filters of all searches, in order (ldapts' string form, escaped like on the wire). */
  filters: string[] = [];
  binds: string[] = [];
  allowAnonymous = true;
  private server?: Server;
  private sockets = new Set<Socket>();
  url = "";

  add(...entries: FakeEntry[]): this {
    this.entries.push(...entries);
    return this;
  }

  async start(): Promise<this> {
    this.server = createServer((s) => this.serve(s));
    await new Promise<void>((r) => this.server!.listen(0, "127.0.0.1", r));
    const addr = this.server.address();
    this.url = `ldap://127.0.0.1:${typeof addr === "object" && addr ? addr.port : 0}`;
    return this;
  }

  async stop(): Promise<void> {
    for (const s of this.sockets) s.destroy();
    await new Promise<void>((r) => (this.server ? this.server.close(() => r()) : r()));
  }

  private values(e: FakeEntry, attr: string): string[] {
    const key = Object.keys(e.attributes).find((k) => k.toLowerCase() === attr.toLowerCase());
    const v = key ? e.attributes[key] : undefined;
    return v === undefined ? [] : Array.isArray(v) ? v : [v];
  }

  private matches(f: Filter, e: FakeEntry): boolean {
    if (f instanceof AndFilter) return f.filters.every((x) => this.matches(x, e));
    if (f instanceof OrFilter) return f.filters.some((x) => this.matches(x, e));
    if (f instanceof NotFilter) return !this.matches(f.filter, e);
    if (f instanceof PresenceFilter) return f.attribute.toLowerCase() === "objectclass" || this.values(e, f.attribute).length > 0;
    if (f instanceof EqualityFilter) {
      const want = String(f.value).toLowerCase();
      return this.values(e, f.attribute).some((v) => v.toLowerCase() === want);
    }
    if (f instanceof SubstringFilter) return this.values(e, f.attribute).some((v) => f.matches({ [f.attribute]: v }));
    return false;
  }

  private serve(socket: Socket): void {
    this.sockets.add(socket);
    socket.on("close", () => this.sockets.delete(socket));
    socket.on("error", () => {});
    let buf = Buffer.alloc(0);
    socket.on("data", (chunk) => {
      buf = Buffer.concat([buf, chunk]);
      for (;;) {
        const reader = new BerReader(buf);
        if (reader.readSequence() === null || reader.remain < reader.length) return;
        const end = reader.offset + reader.length;
        const message = buf.subarray(0, end);
        buf = buf.subarray(end);
        try {
          this.handle(socket, new BerReader(message));
        } catch {
          // Not LDAP (a TLS handshake, garbage): hang up like a real server would.
          socket.destroy();
          return;
        }
      }
    });
  }

  private handle(socket: Socket, reader: BerReader): void {
    reader.readSequence();
    const messageId = reader.readInt()!;
    const op = reader.readSequence();
    if (op === OP.unbind) {
      socket.end();
      return;
    }
    if (op === OP.bind) {
      const req = new BindRequest({ messageId });
      req.parse(reader, []);
      this.binds.push(req.dn);
      const entry = this.entries.find((e) => norm(e.dn) === norm(req.dn));
      let status: number = RESULT.invalidCredentials;
      if (!req.dn && !req.password) status = this.allowAnonymous ? RESULT.success : RESULT.unwilling;
      else if (req.dn && !req.password) status = RESULT.unwilling;
      else if (entry?.password !== undefined && entry.password === req.password) status = RESULT.success;
      socket.write(this.result(messageId, OP.bindRes, status));
      return;
    }
    if (op === OP.search) {
      const req = new SearchRequest({ messageId, filter: new PresenceFilter({ attribute: "objectClass" }) });
      req.parse(reader, []);
      this.filters.push(req.filter.toString());
      const base = norm(req.baseDN);
      const baseEntry = this.entries.find((e) => norm(e.dn) === base);
      const candidates =
        req.scope === "base" ? (baseEntry ? [baseEntry] : []) : this.entries.filter((e) => norm(e.dn) === base || norm(e.dn).endsWith(`,${base}`));
      if (req.scope === "base" && !baseEntry && !this.entries.some((e) => norm(e.dn).endsWith(`,${base}`))) {
        socket.write(this.result(messageId, OP.done, RESULT.noSuchObject));
        return;
      }
      const hits = candidates.filter((e) => this.matches(req.filter, e));
      const limit = req.sizeLimit > 0 ? req.sizeLimit : hits.length;
      for (const e of hits.slice(0, limit)) socket.write(this.entry(messageId, e, req.attributes));
      socket.write(this.result(messageId, OP.done, hits.length > limit ? 4 : RESULT.success));
      return;
    }
    socket.write(this.result(messageId, OP.done, RESULT.unwilling));
  }

  private envelope(messageId: number, body: (w: BerWriter) => void): Buffer {
    const w = new BerWriter();
    w.startSequence();
    w.writeInt(messageId);
    body(w);
    w.endSequence();
    return w.buffer;
  }

  private result(messageId: number, op: number, status: number): Buffer {
    return this.envelope(messageId, (w) => {
      w.startSequence(op);
      w.writeEnumeration(status);
      w.writeString("");
      w.writeString("");
      w.endSequence();
    });
  }

  private entry(messageId: number, e: FakeEntry, attributes: string[]): Buffer {
    const wanted = attributes.filter((a) => a !== "1.1").map((a) => a.toLowerCase());
    return this.envelope(messageId, (w) => {
      w.startSequence(OP.entry);
      w.writeString(e.dn);
      w.startSequence();
      for (const [name, v] of Object.entries(e.attributes)) {
        if (attributes.includes("1.1") || (wanted.length && !wanted.includes(name.toLowerCase()))) continue;
        w.startSequence();
        w.writeString(name);
        w.startSequence(0x31);
        for (const x of Array.isArray(v) ? v : [v]) w.writeString(x);
        w.endSequence();
        w.endSequence();
      }
      w.endSequence();
      w.endSequence();
    });
  }
}

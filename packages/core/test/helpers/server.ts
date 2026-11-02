/**
 * In-process test server: the same route table as Nitro (src/http/routes.ts) mounted on an
 * H3 app and served on an ephemeral port, plus Connect clients for the RPC services.
 */
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { createClient, type Client } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { H3, serve } from "h3";
import { DeviceAuthService, PollDeviceAuthResponse_Status } from "../../src/gen/harness/auth/v1/auth_pb.ts";
import { GatewayService } from "../../src/gen/harness/llm/v1/gateway_pb.ts";
import { PromptService } from "../../src/gen/harness/prompts/v1/prompts_pb.ts";

const ROOT = resolve(import.meta.dirname, "../..");

export const ADMIN = { email: "admin@test.local", password: "admin-password-1" };

export interface TestServer {
  url: string;
  auth: Client<typeof DeviceAuthService>;
  gateway: Client<typeof GatewayService>;
  prompts: Client<typeof PromptService>;
  /** Connect clients authenticated with the given access token. */
  as(token: string): { auth: Client<typeof DeviceAuthService>; gateway: Client<typeof GatewayService>; prompts: Client<typeof PromptService> };
  close(): Promise<void>;
}

export async function startTestServer(env: Record<string, string> = {}): Promise<TestServer> {
  process.env.HARNESS_DATA_DIR = mkdtempSync(join(tmpdir(), "harness-test-"));
  process.env.HARNESS_SECRET = "test-secret-test-secret-test-secret-0123456789";
  process.env.HARNESS_ADMIN_EMAIL = ADMIN.email;
  process.env.HARNESS_ADMIN_PASSWORD = ADMIN.password;
  process.env.HARNESS_UPSTREAM_RETRY_BASE_MS = "5";
  process.env.HARNESS_PROMPTS_DIR = join(ROOT, "prompts");
  // device-flow endpoints are rate limited per IP; every test client is 127.0.0.1
  process.env.HARNESS_DEVICE_RATE_LIMIT_PER_MIN = "10000";
  Object.assign(process.env, env);

  const { routes } = await import("../../src/http/routes.ts");
  const app = new H3();
  for (const r of routes) {
    const mod = await import(resolve(ROOT, r.handler));
    app.on(r.method ?? "", r.route, mod.default);
  }
  const server = serve(app, { port: 0, hostname: "127.0.0.1", silent: true });
  await server.ready();
  const url = server.url!.replace(/\/$/, "");
  process.env.HARNESS_PUBLIC_URL = url;

  const { bootstrap } = await import("../../src/bootstrap.ts");
  await bootstrap();

  const clients = (token?: string) => {
    const transport = createConnectTransport({
      baseUrl: url,
      httpVersion: "1.1",
      interceptors: token
        ? [
            (next) => async (req) => {
              req.header.set("authorization", `Bearer ${token}`);
              return next(req);
            },
          ]
        : [],
    });
    return {
      auth: createClient(DeviceAuthService, transport),
      gateway: createClient(GatewayService, transport),
      prompts: createClient(PromptService, transport),
    };
  };

  return {
    url,
    ...clients(),
    as: (token) => clients(token),
    close: async () => {
      await server.close(true);
    },
  };
}

/** Signs in through Better Auth and returns the session cookie header. */
export async function signIn(url: string, email: string, password: string): Promise<string> {
  const res = await fetch(`${url}/api/auth/sign-in/email`, {
    method: "POST",
    headers: { "content-type": "application/json", origin: url },
    body: JSON.stringify({ email, password }),
  });
  if (!res.ok) throw new Error(`sign-in failed: ${res.status} ${await res.text()}`);
  return res.headers
    .getSetCookie()
    .map((c) => c.split(";")[0])
    .join("; ");
}

/** Approves (or denies) a user code on the /device page as the signed-in user. */
export async function decideOnDevicePage(url: string, cookie: string, userCode: string, action: "approve" | "deny") {
  return fetch(`${url}/device`, {
    method: "POST",
    headers: { cookie, origin: url, "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ code: userCode, action }).toString(),
  });
}

/** Full device flow for a user; returns the credentials. */
export async function deviceLogin(ts: TestServer, email = ADMIN.email, password = ADMIN.password) {
  const cookie = await signIn(ts.url, email, password);
  const start = await ts.auth.startDeviceAuth({ clientName: "harness-client", deviceName: "test-box", os: "linux", arch: "amd64" });
  const res = await decideOnDevicePage(ts.url, cookie, start.userCode, "approve");
  if (!res.ok) throw new Error(`approve failed: ${res.status}`);
  const poll = await ts.auth.pollDeviceAuth({ deviceCode: start.deviceCode });
  if (poll.status !== PollDeviceAuthResponse_Status.APPROVED || !poll.credentials) throw new Error(`poll status ${poll.status}`);
  return { ...poll.credentials, cookie };
}

/** Creates a user through the Better Auth admin plugin (admin session cookie) and logs a device in. */
export async function createUserAndLogin(ts: TestServer, adminCookie: string, email: string, password = "user-password-1", role = "user") {
  const res = await fetch(`${ts.url}/api/auth/admin/create-user`, {
    method: "POST",
    headers: { cookie: adminCookie, origin: ts.url, "content-type": "application/json" },
    body: JSON.stringify({ email, password, name: email.split("@")[0], role }),
  });
  if (!res.ok) throw new Error(`create-user failed: ${res.status} ${await res.text()}`);
  return deviceLogin(ts, email, password);
}

/** JSON call to the admin HTTP API with a bearer token. */
export async function adminApi(url: string, token: string, method: string, path: string, body?: unknown) {
  const res = await fetch(`${url}${path}`, {
    method,
    headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  return { status: res.status, body: text ? JSON.parse(text) : undefined };
}

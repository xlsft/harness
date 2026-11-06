/** SCIM 2.0 endpoints under /scim/v2 (src/scim), authenticated with a provisioning token from Settings. */
import { defineHandler } from "h3";
import { authenticateScim } from "../../scim/tokens.ts";
import * as scim from "../../scim/service.ts";
import { ERROR_SCHEMA, ScimError } from "../../scim/service.ts";

const TYPE = "application/scim+json";

function reply(body: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { "content-type": TYPE, "cache-control": "no-store", ...headers } });
}

function failure(status: number, detail: string, scimType?: string): Response {
  return reply({ schemas: [ERROR_SCHEMA], status: String(status), detail, ...(scimType ? { scimType } : {}) }, status);
}

export default defineHandler(async (event) => {
  const m = /^Bearer\s+(\S+)$/i.exec(event.req.headers.get("authorization")?.trim() ?? "");
  const actor = m ? await authenticateScim(m[1]!) : undefined;
  if (!actor) return failure(401, "A valid provisioning token is required");

  const parts = event.url.pathname.replace(/^\/scim\/v2\/?/, "").split("/").filter(Boolean).map(decodeURIComponent);
  const [resource, id] = parts;
  const method = event.req.method;
  const q = event.url.searchParams;
  const body = async () => {
    const text = await event.req.text();
    try {
      return text ? (JSON.parse(text) as Record<string, unknown>) : {};
    } catch {
      throw new ScimError(400, "The body must be JSON", "invalidSyntax");
    }
  };
  try {
    if (method === "GET" && resource === "ServiceProviderConfig") return reply(scim.serviceProviderConfig());
    if (method === "GET" && resource === "ResourceTypes") return reply(scim.resourceTypes());
    if (method === "GET" && resource === "Schemas") return reply(scim.schemas());
    if (resource === "Users") {
      if (!id && method === "GET") return reply(await scim.listUsers(q));
      if (!id && method === "POST") return reply(await scim.createUser(await body(), actor), 201);
      if (id && method === "GET") return reply(await scim.getUser(id));
      if (id && method === "PUT") return reply(await scim.replaceUser(id, await body(), actor));
      if (id && method === "PATCH") return reply(await scim.patchUser(id, await body(), actor));
      if (id && method === "DELETE") {
        await scim.removeUser(id, actor);
        return reply(undefined, 204);
      }
    }
    if (resource === "Groups") {
      if (!id && method === "GET") return reply(await scim.listGroups(q));
      if (!id && method === "POST") return reply(await scim.createScimGroup(await body(), actor), 201);
      if (id && method === "GET") return reply(await scim.getScimGroup(id));
      if (id && method === "PUT") return reply(await scim.replaceScimGroup(id, await body(), actor));
      if (id && method === "PATCH") return reply(await scim.patchScimGroup(id, await body(), actor));
      if (id && method === "DELETE") {
        await scim.removeScimGroup(id, actor);
        return reply(undefined, 204);
      }
    }
    return failure(404, "No such endpoint");
  } catch (e) {
    if (e instanceof ScimError) return failure(e.status, e.message, e.scimType);
    if (e instanceof Error && "type" in e && (e as { type: string }).type === "conflict") return failure(409, e.message, "uniqueness");
    if (e instanceof Error && "type" in e && (e as { type: string }).type === "not_found") return failure(404, e.message);
    console.error("[harness] SCIM request failed", e);
    return failure(500, "Internal error");
  }
});

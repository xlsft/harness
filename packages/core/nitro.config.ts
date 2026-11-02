import { defineConfig } from "nitro";
import { routes } from "./src/http/routes.ts";

export default defineConfig({
  // Routes are declared in one table (src/http/routes.ts) so tests can mount the same handlers.
  serverDir: false,
  handlers: routes.map((r) => ({
    route: r.route,
    method: r.method?.toUpperCase() as "GET" | "POST" | "PUT" | "PATCH" | "DELETE" | undefined,
    handler: r.handler,
  })),
  plugins: ["./src/http/plugins/bootstrap.ts"],
});

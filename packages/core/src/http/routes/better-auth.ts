import { defineHandler } from "h3";
import { getAuth } from "../../auth/better-auth.ts";

/** Better Auth handles everything else under /api/auth (sign-in, sessions, admin plugin). */
export default defineHandler((event) => getAuth().handler(event.req));

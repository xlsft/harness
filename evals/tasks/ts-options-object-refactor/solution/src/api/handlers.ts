import { createUser } from "../users.ts";

export function postUser(body: { name: string; email: string; admin?: boolean }) {
  return createUser({ name: body.name, email: body.email, role: body.admin ? "admin" : "member" });
}

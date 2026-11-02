import { createUser } from "../users.ts";

export function postUser(body: { name: string; email: string; admin?: boolean }) {
  return createUser(body.name, body.email, body.admin ?? false);
}

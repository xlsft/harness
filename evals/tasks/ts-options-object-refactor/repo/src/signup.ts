import { createUser } from "./users.ts";

export function signup(name: string, email: string) {
  return createUser(name, email, false);
}

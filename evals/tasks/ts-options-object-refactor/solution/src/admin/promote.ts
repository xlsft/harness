import * as users from "../users.ts";

export function makeAdmin(name: string, email: string) {
  return users.createUser({ name, email, role: "admin" });
}

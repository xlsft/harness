import { createUser as mk } from "../users.ts";

export function fromCsv(line: string) {
  const [name, email, admin] = line.split(",");
  return mk(name, email, admin === "yes");
}

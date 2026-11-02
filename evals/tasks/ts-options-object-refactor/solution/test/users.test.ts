import { test } from "node:test";
import assert from "node:assert/strict";
import { createUser } from "../src/users.ts";
import { seedUsers } from "../src/seed.ts";

test("creates members and admins", () => {
  assert.equal(createUser({ name: "A", email: "a@x.io" }).role, "member");
  assert.equal(createUser({ name: "B", email: "b@x.io", role: "admin" }).role, "admin");
  assert.equal(seedUsers.length, 3);
});

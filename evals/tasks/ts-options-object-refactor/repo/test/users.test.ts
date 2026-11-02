import { test } from "node:test";
import assert from "node:assert/strict";
import { createUser } from "../src/users.ts";
import { seedUsers } from "../src/seed.ts";

test("creates members and admins", () => {
  assert.equal(createUser("A", "a@x.io", false).role, "member");
  assert.equal(createUser("B", "b@x.io", true).role, "admin");
  assert.equal(seedUsers.length, 3);
});

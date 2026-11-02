import { test } from "node:test";
import assert from "node:assert/strict";
import { createUser } from "../src/users.ts";
import { seedUsers } from "../src/seed.ts";
import { signup } from "../src/signup.ts";
import { makeAdmin } from "../src/admin/promote.ts";
import { fromCsv } from "../src/import/csv.ts";
import { postUser } from "../src/api/handlers.ts";

test("options object API", () => {
  assert.deepEqual(createUser({ name: "A", email: "a@x.io" }), { name: "A", email: "a@x.io", role: "member" });
  assert.equal(createUser({ name: "B", email: "b@x.io", role: "admin" }).role, "admin");
  assert.throws(() => createUser({ name: "C", email: "nope" }));
});

test("callers keep their behavior", () => {
  assert.deepEqual(seedUsers.map((u) => u.role), ["admin", "member", "member"]);
  assert.equal(signup("S", "s@x.io").role, "member");
  assert.equal(makeAdmin("M", "m@x.io").role, "admin");
  assert.equal(fromCsv("C,c@x.io,yes").role, "admin");
  assert.equal(fromCsv("D,d@x.io,no").role, "member");
  assert.equal(postUser({ name: "P", email: "p@x.io", admin: true }).role, "admin");
  assert.equal(postUser({ name: "Q", email: "q@x.io" }).role, "member");
});

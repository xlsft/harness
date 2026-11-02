import { createUser } from "./users.ts";

export const seedUsers = [
  createUser({ name: "Root", email: "root@example.com", role: "admin" }),
  createUser({ name: "Ann", email: "ann@example.com" }),
  createUser({ name: "Bob", email: "bob@example.com" }),
];

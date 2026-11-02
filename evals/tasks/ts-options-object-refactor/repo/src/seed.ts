import { createUser } from "./users.ts";

export const seedUsers = [
  createUser("Root", "root@example.com", true),
  createUser("Ann", "ann@example.com", false),
  createUser(
    "Bob",
    "bob@example.com",
    false,
  ),
];

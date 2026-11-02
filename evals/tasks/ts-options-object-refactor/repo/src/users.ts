export type Role = "member" | "admin";

export interface User {
  name: string;
  email: string;
  role: Role;
}

/** Creates a user. `isAdmin` selects the admin role. */
export function createUser(name: string, email: string, isAdmin: boolean): User {
  if (!email.includes("@")) throw new Error(`invalid email: ${email}`);
  return { name, email, role: isAdmin ? "admin" : "member" };
}

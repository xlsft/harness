export type Role = "member" | "admin";

export interface User {
  name: string;
  email: string;
  role: Role;
}

export interface CreateUserOptions {
  name: string;
  email: string;
  role?: Role;
}

/** Creates a user. `role` defaults to "member". */
export function createUser({ name, email, role = "member" }: CreateUserOptions): User {
  if (!email.includes("@")) throw new Error(`invalid email: ${email}`);
  return { name, email, role };
}

import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    include: ["test/**/*.test.ts"],
    testTimeout: 30_000,
    hookTimeout: 30_000,
    // Each test file gets its own data dir (see test/helpers/env.ts), so files can run in parallel.
    pool: "forks",
  },
});

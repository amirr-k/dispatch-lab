import { defineConfig, devices } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
// the repo root, since cmd/server lives two levels up from apps/web.
const repoRoot = path.resolve(__dirname, "../..");

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false, // every test drives the same in-memory backend instance
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? "github" : "list",
  use: {
    baseURL: "http://localhost:5173",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
  ],
  // both processes are started fresh per run and torn down after: the
  // backend falls back to an in-memory store with no DATABASE_URL set, so
  // this needs no database and leaves nothing behind.
  webServer: [
    {
      command: "go run ./cmd/server",
      cwd: repoRoot,
      url: "http://localhost:8080/health/live",
      reuseExistingServer: !process.env.CI,
      timeout: 60_000,
      env: { ADDR: ":8080" },
    },
    {
      command: "npm run dev -- --port 5173 --strictPort",
      cwd: __dirname,
      url: "http://localhost:5173",
      reuseExistingServer: !process.env.CI,
      timeout: 30_000,
    },
  ],
});

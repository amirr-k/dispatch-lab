import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// api.ts reads VITE_API_URL at module load time via import.meta.env, so the
// module must be re-imported fresh in each test rather than imported once at
// the top of the file - vitest's module registry reset (vi.resetModules)
// combined with a dynamic import is what makes that possible.
async function freshApi() {
  vi.resetModules();
  return import("./api");
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

describe("ensureSession", () => {
  beforeEach(() => {
    sessionStorage.clear();
  });
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("issues a session and stores the token", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse(201, { token: "tok-1", expiresAt: "2026-01-01T00:00:00Z", simulationQuota: 3 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const api = await freshApi();
    const token = await api.ensureSession();

    expect(token).toBe("tok-1");
    expect(sessionStorage.getItem("dispatchlab.guestToken")).toBe("tok-1");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("reuses a stored token instead of issuing a new one", async () => {
    sessionStorage.setItem("dispatchlab.guestToken", "existing-token");
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    const api = await freshApi();
    const token = await api.ensureSession();

    expect(token).toBe("existing-token");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("deduplicates concurrent calls into a single request", async () => {
    let resolveFetch!: (r: Response) => void;
    const fetchMock = vi.fn().mockReturnValue(new Promise<Response>((resolve) => (resolveFetch = resolve)));
    vi.stubGlobal("fetch", fetchMock);

    const api = await freshApi();
    const first = api.ensureSession();
    const second = api.ensureSession();

    resolveFetch(jsonResponse(201, { token: "tok-race", expiresAt: "", simulationQuota: 3 }));

    expect(await first).toBe("tok-race");
    expect(await second).toBe("tok-race");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("throws ApiError when the server refuses to issue a session", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(503, { error: { message: "no capacity" } })));

    const api = await freshApi();
    await expect(api.ensureSession()).rejects.toThrow(api.ApiError);
  });
});

describe("request retry-on-401", () => {
  beforeEach(() => {
    sessionStorage.clear();
  });
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("clears the stale token, re-authenticates once, and retries the original call", async () => {
    sessionStorage.setItem("dispatchlab.guestToken", "stale-token");

    const calls: string[] = [];
    const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      calls.push(`${init?.method ?? "GET"} ${url} ${(init?.headers as Record<string, string>)?.Authorization}`);
      if (url.endsWith("/api/v1/guest-sessions")) {
        return Promise.resolve(jsonResponse(201, { token: "fresh-token", expiresAt: "", simulationQuota: 3 }));
      }
      // the first attempt (carrying the stale token) is refused; the retry
      // (carrying the fresh one) succeeds.
      if ((init?.headers as Record<string, string>)?.Authorization === "Bearer stale-token") {
        return Promise.resolve(jsonResponse(401, { error: { message: "expired" } }));
      }
      return Promise.resolve(jsonResponse(201, { id: "sim-1", seed: 1, drivers: 12 }));
    });
    vi.stubGlobal("fetch", fetchMock);

    const api = await freshApi();
    const result = await api.createSimulation();

    expect(result).toEqual({ id: "sim-1", seed: 1, drivers: 12 });
    expect(sessionStorage.getItem("dispatchlab.guestToken")).toBe("fresh-token");
    // guest-sessions is only hit once more (the re-auth) - not looped.
    expect(calls.filter((c) => c.includes("guest-sessions"))).toHaveLength(1);
  });

  it("surfaces the server's error message via ApiError for a non-401 failure", async () => {
    sessionStorage.setItem("dispatchlab.guestToken", "tok");
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(jsonResponse(429, { error: { code: "quota", message: "too many runs" } })),
    );

    const api = await freshApi();
    await expect(api.createSimulation()).rejects.toMatchObject({ status: 429, message: "too many runs" });
  });

  it("treats a 202 response as having no body", async () => {
    sessionStorage.setItem("dispatchlab.guestToken", "tok");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(null, { status: 202 })));

    const api = await freshApi();
    await expect(api.placeOrder("sim-1", "n-0-0", "n-1-1")).resolves.toBeUndefined();
  });
});

describe("streamURL", () => {
  beforeEach(() => {
    sessionStorage.clear();
  });
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("rewrites http to ws and carries the token as a query parameter", async () => {
    sessionStorage.setItem("dispatchlab.guestToken", "tok-1");
    const api = await freshApi();

    expect(api.streamURL("sim-1")).toBe("ws://localhost:8080/api/v1/simulations/sim-1/stream?token=tok-1");
  });

  it("omits the token parameter entirely when there is none stored", async () => {
    const api = await freshApi();
    expect(api.streamURL("sim-1")).toBe("ws://localhost:8080/api/v1/simulations/sim-1/stream");
  });
});

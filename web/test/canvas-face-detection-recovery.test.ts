import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { CanvasFaceDetectionTimeoutError, createRetryingFactory, createTimeoutTracker } from "../src/lib/canvas/canvas-face-detection-core";

const read = (path: string) => readFileSync(resolve(import.meta.dir, path), "utf8");
const worker = read("../src/lib/canvas/canvas-face-detector.worker.ts");
const detection = read("../src/lib/canvas/canvas-face-detection.ts");
const workspace = read("../src/components/canvas/canvas-emotion-workspace.tsx");

describe("detector factory retries after a failed creation", () => {
    test("caches a successful detector instead of recreating it per call", async () => {
        let calls = 0;
        const get = createRetryingFactory(async () => {
            calls += 1;
            return { id: calls };
        });

        expect(await get()).toEqual({ id: 1 });
        expect(await get()).toEqual({ id: 1 });
        expect(calls).toBe(1);
    });

    test("does not cache a rejected creation, so the next call can succeed", async () => {
        let calls = 0;
        const get = createRetryingFactory(async () => {
            calls += 1;
            if (calls === 1) throw new Error("Failed to fetch");
            return { id: calls };
        });

        await expect(get()).rejects.toThrow("Failed to fetch");
        // 这就是“人脸识别一直不成功”的回归点：第二次必须真的重试并成功。
        expect(await get()).toEqual({ id: 2 });
        expect(calls).toBe(2);
    });

    test("shares one in-flight creation across concurrent callers", async () => {
        let calls = 0;
        const get = createRetryingFactory(async () => {
            calls += 1;
            await new Promise((r) => setTimeout(r, 5));
            return calls;
        });

        const [a, b, c] = await Promise.all([get(), get(), get()]);
        expect([a, b, c]).toEqual([1, 1, 1]);
        expect(calls).toBe(1);
    });

    test("recovers after a failure even when several callers raced", async () => {
        let calls = 0;
        const get = createRetryingFactory(async () => {
            calls += 1;
            if (calls === 1) throw new Error("boom");
            return "ready";
        });

        const raced = await Promise.allSettled([get(), get(), get()]);
        expect(raced.every((r) => r.status === "rejected")).toBe(true);
        // 三个并发调用共享同一次失败（尝试次数仍是 1），但缓存已清空，下一次必须重新尝试。
        expect(calls).toBe(1);
        expect(await get()).toBe("ready");
        expect(calls).toBe(2);
    });
});

describe("consecutive-timeout tracker", () => {
    test("only reports recycling after the configured number of consecutive timeouts", () => {
        const tracker = createTimeoutTracker(2);
        expect(tracker.recordTimeout()).toBe(false);
        expect(tracker.recordTimeout()).toBe(true);
    });

    test("a success resets the streak so a one-off slow run cannot accumulate", () => {
        const tracker = createTimeoutTracker(2);
        tracker.recordTimeout();
        tracker.recordSuccess();
        expect(tracker.recordTimeout()).toBe(false);
        expect(tracker.recordTimeout()).toBe(true);
    });
});

describe("worker wiring contracts", () => {
    test("worker builds the detector through the retrying factory", () => {
        expect(worker).toContain("createRetryingFactory");
        // 旧实现直接缓存 createFromOptions 的 promise，失败后永久锁死。
        expect(worker).not.toMatch(/detectorPromise\s*=\s*FaceDetector\.createFromOptions/);
    });

    test("worker bounds the loader fetch so a hung request cannot stall forever", () => {
        expect(worker).toContain("AbortSignal.timeout");
    });
});

describe("main-thread wiring contracts", () => {
    test("detection has a hard timeout and rejects with a recoverable error", () => {
        expect(detection).toContain("DETECT_TIMEOUT_MS");
        expect(detection).toContain("CanvasFaceDetectionTimeoutError");
        expect(new CanvasFaceDetectionTimeoutError().name).toBe("CanvasFaceDetectionTimeoutError");
    });

    test("timed-out or erroring workers are recycled and inflight requests rejected", () => {
        expect(detection).toContain("recycleDetectorWorker");
        expect(detection).toContain("terminate()");
        expect(detection).toContain("timeoutTracker.recordSuccess()");
    });

    test("a settled request never resolves twice or after its timeout", () => {
        expect(detection).toContain("if (settled) return;");
        expect(detection).toContain("clearTimeout(timer)");
    });
});

describe("emotion workspace never traps the user in a spinner", () => {
    test("manual selection becomes available when detection runs slow", () => {
        expect(workspace).toContain("SLOW_DETECTING_HINT_MS");
        expect(workspace).toContain("slowDetecting");
        expect(workspace).toContain('const showManualAction = status !== "detecting" || slowDetecting;');
    });

    test("a failed detection exposes a retry action instead of forcing a reopen", () => {
        expect(workspace).toContain("onRetryDetect");
        expect(workspace).toContain("重新识别");
        expect(workspace).toContain("setDetectAttempt");
    });

    test("retrying re-runs detection for the same image", () => {
        expect(workspace).toMatch(/\[dataUrl, detectAttempt\]/);
    });
});

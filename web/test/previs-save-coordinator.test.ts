import { describe, expect, it } from "bun:test";

import type { PrevisScene } from "../src/types/previs";
import { createPrevisSaveCoordinator } from "../src/lib/canvas/previs/previs-save";

function scene(id: string, title: string, updatedAt = "2026-01-01T00:00:00.000Z"): PrevisScene {
    return {
        id,
        version: 1,
        title,
        background: "#101010",
        environmentIntensity: 1,
        gridVisible: true,
        objects: [],
        cameras: [],
        lights: [],
        shots: [],
        activeShotId: "",
        createdAt: "2026-01-01T00:00:00.000Z",
        updatedAt,
    };
}

/** 用可控的调度器替代真实计时器，测试不依赖墙钟。 */
function manualScheduler() {
    const pending: Array<() => void> = [];
    return {
        schedule: (callback: () => void) => {
            pending.push(callback);
            return pending.length - 1;
        },
        cancelSchedule: (handle: unknown) => {
            const index = handle as number;
            if (index >= 0 && index < pending.length) pending[index] = () => undefined;
        },
        runPending: () => {
            const queued = pending.splice(0, pending.length);
            for (const callback of queued) callback();
        },
        get size() {
            return pending.length;
        },
    };
}

describe("PrevisSaveCoordinator", () => {
    it("starts saved with revision zero", () => {
        const coordinator = createPrevisSaveCoordinator({
            initialScene: scene("scene-1", "初始"),
            flush: async () => undefined,
        });
        expect(coordinator.getSnapshot()).toEqual({ revision: 0, confirmedRevision: 0, status: "saved" });
        coordinator.dispose();
    });

    it("rejects edits for a foreign scene id without touching state", () => {
        const coordinator = createPrevisSaveCoordinator({
            initialScene: scene("scene-1", "初始"),
            flush: async () => undefined,
        });
        expect(coordinator.edit(scene("scene-2", "别的场景"))).toBe(false);
        expect(coordinator.getSnapshot()).toEqual({ revision: 0, confirmedRevision: 0, status: "saved" });
        coordinator.dispose();
    });

    it("marks dirty on edit and flushes through the debounced schedule", async () => {
        const scheduler = manualScheduler();
        const flushed: number[] = [];
        const coordinator = createPrevisSaveCoordinator({
            initialScene: scene("scene-1", "初始"),
            flush: async (request) => {
                flushed.push(request.revision);
            },
            schedule: scheduler.schedule,
            cancelSchedule: scheduler.cancelSchedule,
        });
        expect(coordinator.edit(scene("scene-1", "改一"))).toBe(true);
        expect(coordinator.getSnapshot().status).toBe("dirty");
        expect(coordinator.getSnapshot().revision).toBe(1);
        expect(scheduler.size).toBe(1);

        scheduler.runPending();
        await Promise.resolve();
        await new Promise((resolve) => setTimeout(resolve, 0));
        expect(flushed).toEqual([1]);
        expect(coordinator.getSnapshot()).toEqual({ revision: 1, confirmedRevision: 1, status: "saved" });
        coordinator.dispose();
    });

    it("keeps the error status and unconfirmed revision when flush fails", async () => {
        const coordinator = createPrevisSaveCoordinator({
            initialScene: scene("scene-1", "初始"),
            flush: async () => {
                throw new Error("boom");
            },
        });
        coordinator.edit(scene("scene-1", "改一"));
        expect(await coordinator.flushLatest()).toBe(false);
        expect(coordinator.getSnapshot()).toEqual({ revision: 1, confirmedRevision: 0, status: "error" });
        coordinator.dispose();
    });

    it("coalesces concurrent edits into one in-flight drain that confirms the latest revision", async () => {
        let release: (() => void) | null = null;
        const gate = new Promise<void>((resolve) => {
            release = resolve;
        });
        const flushedRevisions: number[] = [];
        const coordinator = createPrevisSaveCoordinator({
            initialScene: scene("scene-1", "初始"),
            flush: async (request) => {
                flushedRevisions.push(request.revision);
                if (request.revision === 1) await gate;
            },
        });
        coordinator.edit(scene("scene-1", "改一"));
        const first = coordinator.flushLatest();
        coordinator.edit(scene("scene-1", "改二"));
        const second = coordinator.flushLatest();
        release?.();
        expect(await first).toBe(true);
        expect(await second).toBe(true);
        expect(flushedRevisions).toEqual([1, 2]);
        expect(coordinator.getSnapshot()).toEqual({ revision: 2, confirmedRevision: 2, status: "saved" });
        coordinator.dispose();
    });

    it("publishes the dirty/saving/saved status sequence and supports unsubscribe", async () => {
        const seen: string[] = [];
        let release: (() => void) | null = null;
        const gate = new Promise<void>((resolve) => {
            release = resolve;
        });
        const coordinator = createPrevisSaveCoordinator({
            initialScene: scene("scene-1", "初始"),
            flush: async () => {
                await gate;
            },
        });
        const unsubscribe = coordinator.onStatusChange((status) => seen.push(status));
        coordinator.edit(scene("scene-1", "改一"));
        const flush = coordinator.flushLatest();
        release?.();
        await flush;
        expect(seen).toEqual(["dirty", "saving", "saved"]);
        unsubscribe();
        coordinator.edit(scene("scene-1", "改二"));
        expect(seen).toEqual(["dirty", "saving", "saved"]);
        coordinator.dispose();
    });

    it("prepareClose only closes once pending edits are confirmed", async () => {
        let release: (() => void) | null = null;
        const gate = new Promise<void>((resolve) => {
            release = resolve;
        });
        const coordinator = createPrevisSaveCoordinator({
            initialScene: scene("scene-1", "初始"),
            flush: async () => {
                await gate;
            },
        });
        expect(await coordinator.prepareClose()).toBe("close");
        coordinator.edit(scene("scene-1", "改一"));
        const close = coordinator.prepareClose();
        release?.();
        expect(await close).toBe("close");
        coordinator.dispose();
    });

    it("prepareClose stays when the pending flush cannot be confirmed", async () => {
        const coordinator = createPrevisSaveCoordinator({
            initialScene: scene("scene-1", "初始"),
            flush: async () => {
                throw new Error("offline");
            },
        });
        coordinator.edit(scene("scene-1", "改一"));
        expect(await coordinator.prepareClose()).toBe("stay");
        coordinator.dispose();
    });

    it("dispose stops scheduling, rejects later edits and stops publishing status", async () => {
        const scheduler = manualScheduler();
        let flushes = 0;
        const seen: string[] = [];
        const coordinator = createPrevisSaveCoordinator({
            initialScene: scene("scene-1", "初始"),
            flush: async () => {
                flushes += 1;
            },
            schedule: scheduler.schedule,
            cancelSchedule: scheduler.cancelSchedule,
        });
        coordinator.onStatusChange((status) => seen.push(status));
        coordinator.edit(scene("scene-1", "改一"));
        coordinator.dispose();
        scheduler.runPending();
        await new Promise((resolve) => setTimeout(resolve, 0));
        expect(flushes).toBe(0);
        expect(coordinator.edit(scene("scene-1", "改二"))).toBe(false);
        expect(await coordinator.flushLatest()).toBe(false);
        expect(seen).toEqual(["dirty"]);
    });
});

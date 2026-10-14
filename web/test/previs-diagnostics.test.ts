import { describe, expect, test } from "bun:test";

import {
    PREVIS_DIAGNOSTIC_CODES,
    PREVIS_SAVE_OUTCOMES,
    formatPrevisDiagnosticCode,
    isPrevisDiagnosticCode,
    previsDiagnosticObjectKind,
    projectPrevisDiagnostic,
} from "../src/lib/canvas/previs/previs-diagnostics";

/** 任何一个都不允许出现在投影输出里。 */
const LEAK_PROBES = [
    "https://cdn.example.com/actor.glb?token=abc123",
    "http://localhost:3000/api/tasks?authorization=Bearer%20xyz",
    "Bearer eyJhbGciOiJIUzI1NiJ9.payload.signature",
    "authorization: Bearer secret-token",
    "cookie=session=deadbeef; Path=/",
    "api_key=sk-live-0000",
];

describe("previs diagnostics projection", () => {
    test("keeps every whitelisted code discoverable and rejects unknown codes", () => {
        for (const code of PREVIS_DIAGNOSTIC_CODES) {
            expect(isPrevisDiagnosticCode(code)).toBe(true);
            expect(projectPrevisDiagnostic(code, {})?.code).toBe(code);
        }
        expect(isPrevisDiagnosticCode("PREVIS_NOT_A_REAL_CODE")).toBe(false);
        expect(projectPrevisDiagnostic("PREVIS_NOT_A_REAL_CODE", {})).toBeNull();
    });

    test("does not leak URLs, credentials or free text through projected fields", () => {
        for (const probe of LEAK_PROBES) {
            const projected = projectPrevisDiagnostic("PREVIS_SAVE_FLUSH_FAILED", {
                sceneId: probe,
                objectId: probe,
                message: probe,
                error: probe,
            });
            const serialized = formatPrevisDiagnosticCode(projected!) + " " + JSON.stringify(projected);
            expect(serialized).not.toContain(probe);
        }
    });

    test("accepts only whitelisted structured fields with validated values", () => {
        const projected = projectPrevisDiagnostic("PREVIS_SAVE_FLUSH_FAILED", {
            revision: 0,
            attempt: 2,
            userInitiated: false,
            saveOutcome: "stay",
            objectKind: "actor",
            sceneId: "scene-1",
        });
        expect(projected?.fields.revision).toBe(0);
        expect(projected?.fields.attempt).toBe(2);
        expect(projected?.fields.userInitiated).toBe(false);
        expect(projected?.fields.saveOutcome).toBe("stay");
        expect(projected?.fields.objectKind).toBe("actor");
        expect(projected?.fields.sceneId).toBe("scene-1");
        expect(formatPrevisDiagnosticCode(projected!)).toContain("revision=0");
    });

    test("drops invalid numeric, boolean and enum values instead of coercing", () => {
        const projected = projectPrevisDiagnostic("PREVIS_SAVE_FLUSH_FAILED", {
            revision: -1,
            attempt: 1000,
            userInitiated: "yes",
            saveOutcome: "offer-draft-exit",
            objectKind: "spaceship",
        });
        expect(projected?.fields.revision).toBeUndefined();
        expect(projected?.fields.attempt).toBeUndefined();
        expect(projected?.fields.userInitiated).toBeUndefined();
        expect(projected?.fields.saveOutcome).toBeUndefined();
        expect(projected?.fields.objectKind).toBeUndefined();
    });

    test("exposes the narrowed close-decision vocabulary and object kinds", () => {
        expect([...PREVIS_SAVE_OUTCOMES]).toEqual(["close", "stay"]);
        expect(previsDiagnosticObjectKind({ kind: "actor" })).toBe("actor");
        expect(previsDiagnosticObjectKind({ kind: "spaceship" })).toBe("unknown");
        expect(previsDiagnosticObjectKind({ primitive: "box" })).toBe("primitive");
        expect(previsDiagnosticObjectKind(null)).toBe("unknown");
    });
});

import { nanoid } from "nanoid";

/**
 * Stable replacement for `crypto.randomUUID()`.
 *
 * `randomUUID` is only exposed in secure contexts. Production deployments that
 * are temporarily accessed through plain HTTP or an IP address therefore crash
 * at task submission time even though `crypto.getRandomValues` remains usable.
 * Keep the UUID-shaped output for server-side diagnostics and idempotency keys.
 */
export function createSecureId(): string {
    const cryptoRef = globalThis.crypto;
    if (typeof cryptoRef?.randomUUID === "function") return cryptoRef.randomUUID();
    return nanoid(32);
}

export function createClientId() {
    return nanoid();
}

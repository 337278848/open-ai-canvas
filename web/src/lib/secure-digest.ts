const fallbackDigestCache = new WeakMap<ArrayBuffer, string>();

function fallbackDigest(bytes: ArrayBuffer | Uint8Array): string {
    // FNV-1a is not cryptographic, but it preserves exact input identity for
    // retry/idempotency keys when Web Crypto is unavailable (plain HTTP).
    const view = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
    let hash = 0x811c9dc5;
    for (const byte of view) {
        hash ^= byte;
        hash = Math.imul(hash, 0x01000193) >>> 0;
    }
    return `fnv1a:${hash.toString(16).padStart(8, "0")}:${view.length}`;
}

export async function sha256Hex(input: string | ArrayBuffer | Uint8Array, subtle: SubtleCrypto | null | undefined = globalThis.crypto?.subtle): Promise<string> {
    const bytes = typeof input === "string" ? new TextEncoder().encode(input) : input;
    const buffer = bytes instanceof Uint8Array ? (bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer) : bytes;
    if (subtle) {
        const digest = await subtle.digest("SHA-256", buffer);
        return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
    }
    if (bytes instanceof Uint8Array) {
        const cached = fallbackDigestCache.get(buffer);
        if (cached) return cached;
    }
    const result = fallbackDigest(buffer);
    if (bytes instanceof Uint8Array) fallbackDigestCache.set(buffer, result);
    return result;
}

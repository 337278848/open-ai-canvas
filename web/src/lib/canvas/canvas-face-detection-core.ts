/**
 * 人脸识别的可测核心策略。
 *
 * 这里只放纯逻辑，避免把“失败后能否重试”这类关键行为埋进 Worker / DOM 依赖里，
 * 以便在 bun test 中直接做回归断言。
 */

/**
 * 只缓存成功结果的惰性工厂。
 *
 * MediaPipe 的 FaceDetector 只应创建一次并复用；但**创建失败绝不能被缓存**：
 * 一旦把 rejected promise 留在缓存里，本次会话后续每次调用都会立刻失败，
 * 用户只能刷新页面才能恢复（这正是“人脸识别一直不成功”的根因）。
 * 失败时清空缓存，让下一次调用真正重试。
 */
export function createRetryingFactory<T>(factory: () => Promise<T>): () => Promise<T> {
    let pending: Promise<T> | null = null;
    return () => {
        if (!pending) {
            pending = factory().catch((error: unknown) => {
                pending = null;
                throw error;
            });
        }
        return pending;
    };
}

/**
 * 连续超时计数器：用于区分“偶发的一次慢”与“Worker 已经真的卡死”。
 * 只有连续超时达到阈值才回收 Worker，避免把仍在进行中的慢速下载打断重启。
 */
export function createTimeoutTracker(limit: number) {
    const threshold = Math.max(1, Math.floor(limit));
    let consecutive = 0;
    return {
        recordTimeout(): boolean {
            consecutive += 1;
            return consecutive >= threshold;
        },
        recordSuccess(): void {
            consecutive = 0;
        },
        get consecutiveTimeouts(): number {
            return consecutive;
        },
    };
}

export class CanvasFaceDetectionTimeoutError extends Error {
    constructor(message = "人脸识别超时，请重试") {
        super(message);
        this.name = "CanvasFaceDetectionTimeoutError";
    }
}

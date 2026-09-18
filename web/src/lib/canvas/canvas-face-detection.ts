import type { CanvasFaceBox } from "./canvas-emotion";
import { CanvasFaceDetectionTimeoutError, createTimeoutTracker } from "./canvas-face-detection-core";

type DetectFaceResponse = {
    id: number;
    faces?: CanvasFaceBox[];
    imageWidth?: number;
    imageHeight?: number;
    error?: string;
};

type PendingRequest = {
    resolve: (result: CanvasFaceDetectionResult) => void;
    reject: (error: Error) => void;
    cleanup: () => void;
};

export type CanvasFaceDetectionResult = {
    faces: CanvasFaceBox[];
    imageWidth: number;
    imageHeight: number;
};

/**
 * 单次识别的兜底超时。
 *
 * Worker 内部的 fetch（wasm / 模型）如果被挂起且不报错，整条链路永远不会 settle，
 * 界面上就会一直停在“正在识别人脸”且没有任何出口。
 * 首次加载要下载约 11MB wasm，所以给足时间；超时后至少让上层能拿到明确失败并给出退路。
 */
const DETECT_TIMEOUT_MS = 45000;

/**
 * 连续超时才会回收 Worker。
 *
 * 第一次超时大概率是慢网络：此时不打断 Worker，若资源随后加载成功会被缓存，重试即刻可用。
 * 连续超时才说明 Worker 真的卡死，此时 terminate 才能中断那个永不返回的 fetch。
 */
const CONSECUTIVE_TIMEOUTS_BEFORE_RECYCLE = 2;

let detectorWorker: Worker | null = null;
let requestSequence = 0;
const pendingRequests = new Map<number, PendingRequest>();
const timeoutTracker = createTimeoutTracker(CONSECUTIVE_TIMEOUTS_BEFORE_RECYCLE);

export async function detectCanvasFaces(dataUrl: string, signal?: AbortSignal): Promise<CanvasFaceDetectionResult> {
    if (signal?.aborted) throw new DOMException("人脸识别已取消", "AbortError");
    const response = await fetch(dataUrl, { signal });
    if (!response.ok) throw new Error("无法读取源图片，请重新上传后再试");
    const image = await createImageBitmap(await response.blob());
    const worker = getDetectorWorker();
    const id = ++requestSequence;
    return new Promise((resolve, reject) => {
        let settled = false;
        const timer = setTimeout(() => {
            if (settled) return;
            settled = true;
            const request = pendingRequests.get(id);
            pendingRequests.delete(id);
            request?.cleanup();
            // 连续超时才回收 Worker：第一次超时保留它，慢速加载完成后仍可命中缓存。
            if (timeoutTracker.recordTimeout()) recycleDetectorWorker();
            reject(new CanvasFaceDetectionTimeoutError());
        }, DETECT_TIMEOUT_MS);
        const settle = (fn: () => void) => {
            if (settled) return;
            settled = true;
            clearTimeout(timer);
            fn();
        };
        const abort = () => {
            const request = pendingRequests.get(id);
            if (!request) return;
            settle(() => {
                pendingRequests.delete(id);
                request.cleanup();
                reject(new DOMException("人脸识别已取消", "AbortError"));
            });
        };
        const cleanup = () => {
            clearTimeout(timer);
            signal?.removeEventListener("abort", abort);
        };
        pendingRequests.set(id, {
            resolve: (result) => settle(() => resolve(result)),
            reject: (error) => settle(() => reject(error)),
            cleanup,
        });
        signal?.addEventListener("abort", abort, { once: true });
        worker.postMessage({ id, image }, [image]);
    });
}

/**
 * 回收 Worker（以及它内部卡住的 fetch 和已创建的检测器），下次调用会重建。
 *
 * 被回收 Worker 上仍未结束的请求必须立刻失败，否则它们只能各自干等到自己的超时。
 */
function recycleDetectorWorker(reason?: Error) {
    const worker = detectorWorker;
    detectorWorker = null;
    if (worker) worker.terminate();
    if (pendingRequests.size) {
        const error = reason || new CanvasFaceDetectionTimeoutError();
        const inflight = [...pendingRequests.values()];
        pendingRequests.clear();
        inflight.forEach((request) => request.reject(error));
    }
}

function getDetectorWorker() {
    if (detectorWorker) return detectorWorker;
    const worker = new Worker(new URL("./canvas-face-detector.worker.ts", import.meta.url), { type: "module" });
    worker.onmessage = (event: MessageEvent<DetectFaceResponse>) => {
        const request = pendingRequests.get(event.data.id);
        if (!request) return;
        pendingRequests.delete(event.data.id);
        request.cleanup();
        if (event.data.error) {
            request.reject(new Error(event.data.error));
            return;
        }
        timeoutTracker.recordSuccess();
        request.resolve({
            faces: event.data.faces || [],
            imageWidth: event.data.imageWidth || 0,
            imageHeight: event.data.imageHeight || 0,
        });
    };
    worker.onerror = (event) => {
        const error = new Error(event.message || "人脸识别服务初始化失败");
        recycleDetectorWorker(error);
    };
    detectorWorker = worker;
    return worker;
}

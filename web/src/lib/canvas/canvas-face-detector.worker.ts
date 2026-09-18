/// <reference lib="webworker" />

import { FaceDetector } from "@mediapipe/tasks-vision";

import type { CanvasFaceBox } from "./canvas-emotion";
import { createRetryingFactory } from "./canvas-face-detection-core";

type DetectFaceRequest = {
    id: number;
    image: ImageBitmap;
};

type DetectFaceResponse = {
    id: number;
    faces?: CanvasFaceBox[];
    imageWidth?: number;
    imageHeight?: number;
    error?: string;
};

/**
 * 加载 loader 脚本的超时时间：卡住的 fetch 会让整条识别链路永远不返回。
 */
const LOADER_TIMEOUT_MS = 20000;

const workerGlobal = self as typeof self & {
    importScripts: (...urls: string[]) => void;
    import?: (url: string) => Promise<unknown>;
};

// MediaPipe 会在模块 Worker 中从 importScripts 失败分支转向 self.import。
// 这里显式标记为运行时 URL，避免 Vite 将 public loader 当作源码模块转换。
workerGlobal.importScripts = () => { throw new TypeError("module worker uses dynamic import"); };
workerGlobal.import = async (url: string) => {
    let response: Response;
    try {
        response = await fetch(url.replace(/\?import(?:&.*)?$/, ""), { signal: AbortSignal.timeout(LOADER_TIMEOUT_MS) });
    } catch (error) {
        if (error instanceof DOMException && error.name === "TimeoutError") {
            throw new Error("人脸识别组件加载超时，请检查网络后重试");
        }
        throw error;
    }
    if (!response.ok) throw new Error(`人脸识别组件加载失败（HTTP ${response.status}），请重试`);
    const blobUrl = URL.createObjectURL(new Blob([await response.text()], { type: "text/javascript" }));
    try {
        return await import(/* @vite-ignore */ blobUrl);
    } finally {
        URL.revokeObjectURL(blobUrl);
    }
};

/**
 * 只缓存成功创建出来的检测器。
 * 创建失败（wasm / 模型瞬时拉取失败、离线抖动等）必须清空缓存，
 * 否则本次会话后续每次识别都会复用同一个 rejected promise，永久失败。
 */
const getDetector = createRetryingFactory(() =>
    FaceDetector.createFromOptions(
        {
            wasmLoaderPath: "/mediapipe/wasm/vision_wasm_module_internal.js",
            wasmBinaryPath: "/mediapipe/wasm/vision_wasm_module_internal.wasm",
        },
        {
            baseOptions: { modelAssetPath: "/canvas/models/blaze-face-full-range-sparse.tflite" },
            runningMode: "IMAGE",
            minDetectionConfidence: 0.25,
            minSuppressionThreshold: 0.3,
        },
    ),
);

self.onmessage = async (event: MessageEvent<DetectFaceRequest>) => {
    const { id, image } = event.data;
    const response: DetectFaceResponse = { id, imageWidth: image.width, imageHeight: image.height };
    try {
        const detector = await getDetector();
        response.faces = detector.detect(image).detections.flatMap((detection, index) => {
            const box = detection.boundingBox;
            if (!box) return [];
            return [{
                id: `face-${id}-${index}`,
                x: box.originX,
                y: box.originY,
                width: box.width,
                height: box.height,
                confidence: detection.categories[0]?.score,
                source: "detected" as const,
            }];
        });
    } catch (error) {
        response.error = error instanceof Error ? error.message : "人脸识别失败";
    } finally {
        image.close();
    }
    self.postMessage(response);
};

export {};

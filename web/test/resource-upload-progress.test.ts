import { expect, test } from "bun:test";
import type { AxiosProgressEvent } from "axios";
import { apiClient } from "../src/services/api/request";
import { uploadResourceFile } from "../src/services/api/resources";

test("普通 multipart 将传输进度换算为文件字节，总量未知不伪造百分比", async () => {
    const adapter = apiClient.defaults.adapter;
    const progress: number[][] = [];
    try {
        apiClient.defaults.adapter = async (config) => {
            config.onUploadProgress?.({ loaded: 100, total: undefined } as AxiosProgressEvent);
            config.onUploadProgress?.({ loaded: 250, total: 1000 } as AxiosProgressEvent);
            config.onUploadProgress?.({ loaded: 1000, total: 1000 } as AxiosProgressEvent);
            return { config, status: 200, statusText: "OK", headers: {}, data: { code: 0, data: { resource: { id: "upload-test" } } } };
        };
        await uploadResourceFile(new Blob([new Uint8Array(400)]), "file", undefined, (loaded, total) => progress.push([loaded, total]));
        expect(progress).toEqual([[100, 400], [400, 400]]);
    } finally {
        apiClient.defaults.adapter = adapter;
    }
});

test("分片上传累加片内真实进度，传输 100% 后仍须等待合并响应", async () => {
    const adapter = apiClient.defaults.adapter;
    const size = 51 * 1024 * 1024;
    const chunkSize = 32 * 1024 * 1024;
    const progress: number[][] = [];
    let merged = false;
    try {
        apiClient.defaults.adapter = async (config) => {
            let data: unknown;
            if (config.url === "/resources/uploads") data = { uploadId: "session", chunkSize, chunkCount: 2 };
            else if (config.url?.endsWith("/complete")) {
                expect(progress.at(-1)).toEqual([size, size]);
                merged = true;
                data = { resource: { id: "chunk-test" } };
            } else {
                const index = Number(config.url?.split("/").at(-1));
                const bytes = index === 0 ? chunkSize : size - chunkSize;
                config.onUploadProgress?.({ loaded: bytes / 2, total: bytes } as AxiosProgressEvent);
                data = { index };
            }
            return { config, status: 200, statusText: "OK", headers: {}, data: { code: 0, data } };
        };
        await uploadResourceFile(new Blob([new Uint8Array(size)]), "video", undefined, (loaded, total) => progress.push([loaded, total]));
        expect(progress).toEqual([[chunkSize / 2, size], [chunkSize, size], [chunkSize + (size - chunkSize) / 2, size], [size, size]]);
        expect(merged).toBe(true);
    } finally {
        apiClient.defaults.adapter = adapter;
    }
});

test("传输结束不吞掉服务端保存错误", async () => {
    const adapter = apiClient.defaults.adapter;
    try {
        apiClient.defaults.adapter = async (config) => {
            config.onUploadProgress?.({ loaded: 100, total: 100 } as AxiosProgressEvent);
            return { config, status: 200, statusText: "OK", headers: {}, data: { code: 9, msg: "保存失败" } };
        };
        await expect(uploadResourceFile(new Blob(["text"]), "file", undefined, () => {})).rejects.toThrow("保存失败");
    } finally {
        apiClient.defaults.adapter = adapter;
    }
});

test("取消 multipart 上传时透传 AbortSignal 且不会退回为本地成功", async () => {
    const adapter = apiClient.defaults.adapter;
    const controller = new AbortController();
    let receivedSignal: AbortSignal | undefined;
    try {
        apiClient.defaults.adapter = async (config) => {
            receivedSignal = config.signal;
            controller.abort();
            throw new DOMException("请求已取消", "AbortError");
        };
        await expect(uploadResourceFile(new Blob(["poster"]), "image", undefined, undefined, controller.signal)).rejects.toMatchObject({ name: "AbortError" });
        expect(receivedSignal).toBe(controller.signal);
    } finally {
        apiClient.defaults.adapter = adapter;
    }
});

test("取消原因保持原始 AbortError 身份，不被资源层重新包装", async () => {
    const adapter = apiClient.defaults.adapter;
    const controller = new AbortController();
    const reason = new DOMException("用户停止上传", "AbortError");
    try {
        apiClient.defaults.adapter = async (config) => {
            controller.abort(reason);
            throw new Error("transport interrupted");
        };
        await expect(uploadResourceFile(new Blob(["poster"]), "image", undefined, undefined, controller.signal)).rejects.toBe(reason);
    } finally {
        apiClient.defaults.adapter = adapter;
    }
});

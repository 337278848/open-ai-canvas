export const chapters = [
    { id: "opening", label: "序幕", title: "创作空间", subtitle: "让一个故事，从文字走向银幕。", description: "面向 AI 影视与短剧创作的工作台。" },
    { id: "story", label: "故事", title: "一念，成故事。" },
    { id: "world", label: "角色", title: "让想象，有了面孔。" },
    { id: "shots", label: "分镜", title: "字里行间，皆是镜头。" },
    { id: "motion", label: "生成", title: "这一刻，动起来。" },
    { id: "canvas", label: "画布", title: "一个世界，一张画布。" },
] as const;

export type WelcomeLook = {
    id: string;
    label: string;
    title: string;
    frames: string[];
    screenplay: string[];
    video?: string;
    credit?: string;
};

export const welcomeLooks: WelcomeLook[] = [
    {
        id: "default", label: "创作空间", title: "开始创作",
        frames: [], screenplay: [],
    },
];

export function getWelcomeLook(search = window.location.search) {
    const id = new URLSearchParams(search).get("look");
    return welcomeLooks.find((look) => look.id === id) ?? welcomeLooks[0];
}

export const showcases = [
    { name: "自由画布", image: "", description: "把灵感连成作品。", detail: "整理参考、连接节点、比较结果，沿着自己的思路继续创作。", href: "/canvas" },
    { name: "即时创作", image: "", description: "从一句话开始。", detail: "选择模型与参考素材，在对话中逐步完成图片和视频。", href: "/create" },
    { name: "项目工作台", image: "", description: "让长故事有条理。", detail: "围绕章节、人物与分镜组织制作，随时回到正在推进的故事。", href: "/projects" },
] as const;

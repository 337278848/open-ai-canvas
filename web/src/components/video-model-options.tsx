// Video-only picker: filter by the actual creation intent, retaining an
// explanatory view of incompatible choices rather than silently adapting input.
import { useMemo, useState } from "react";
import { Check, Search } from "lucide-react";
import { assessModelApplicability } from "@/lib/model-applicability";
import { configuredModelDisplayName, type ModelRequirements } from "@/lib/model-selection";
import { modelChannelLabel } from "@/lib/model-picker-groups";
import { modelIcon, type AiConfig } from "@/stores/use-config-store";
import { ModelLogo } from "@/components/model-logo";
import "./model-capability.css";

type Props = {
    config: AiConfig;
    models: string[];
    current: string;
    requirements?: ModelRequirements;
    showPrices: boolean;
    onSelect: (model: string) => void;
};

export function VideoModelOptions({ config, models, current, requirements, showPrices, onSelect }: Props) {
    const [showAll, setShowAll] = useState(false);
    const [search, setSearch] = useState("");
    const items = useMemo(() => models.map((model) => ({
        model, name: configuredModelDisplayName(config, model), channel: modelChannelLabel(config, model),
        assessment: assessModelApplicability(config, model, { ...requirements, capability: "video" }),
    })), [config, models, requirements]);
    const readyCount = items.filter((item) => item.assessment.status === "ready").length;
    const query = search.trim().toLowerCase();
    const visible = items.filter((item) => `${item.name} ${item.channel}`.toLowerCase().includes(query) && (showAll || item.assessment.status === "ready"));
    const sections = [
        { status: "ready", label: "可直接使用" },
        { status: "needs_input", label: "补充素材后可用" },
        { status: "incompatible", label: "当前条件不适用" },
        { status: "unknown", label: "能力待确认" },
    ] as const;
    return <div className="video-model-options">
        <div className="video-model-filter">
            <label><Search aria-hidden="true" /><input aria-label="搜索视频模型或渠道" placeholder="搜索模型或渠道" value={search} onChange={(event) => setSearch(event.target.value)} /></label>
            <button type="button" aria-pressed={!showAll} onClick={() => setShowAll((value) => !value)}>
                {showAll ? `仅看适用 ${readyCount}` : `查看全部 ${items.length}`}
            </button>
        </div>
        <div className="video-model-filter-meta" role="status">
            <span>{readyCount} 个可用</span>
            <span aria-hidden="true">·</span>
            <span>{showPrices ? "按预计费用排序" : "按适用性筛选"}</span>
        </div>
        <div className="video-model-results">
            {!visible.length ? <div className="video-model-empty" role="status">
                <strong>{query ? "没有匹配的模型" : "没有满足当前全部条件的模型"}</strong>
                <span>素材与规格已保留，可查看全部模型。</span>
                {!showAll ? <button type="button" onClick={() => setShowAll(true)}>查看全部模型及原因</button> : null}
            </div> : null}
            {sections.map((section) => {
                const rows = visible.filter((item) => item.assessment.status === section.status)
                    .sort((a, b) => (showPrices ? (a.assessment.estimatedCredits ?? Infinity) - (b.assessment.estimatedCredits ?? Infinity) : 0) || a.name.localeCompare(b.name, "zh-CN"));
                return rows.length ? <section key={section.status} className="video-model-section" aria-label={section.label}>
                    <h3>{showAll || section.status !== "ready" ? `${section.label} · ${rows.length}` : `推荐 · ${rows.length}`}</h3>
                    {rows.map(({ model, name, channel, assessment }) => {
                        const selectable = assessment.status === "ready" || assessment.status === "needs_input";
                        const selected = model === current;
                        return <button
                            key={model} type="button" role="option" data-model-picker-item
                            className="video-model-option" data-state={assessment.status}
                            aria-selected={selected} aria-disabled={!selectable} disabled={!selectable}
                            onClick={() => { if (selectable) onSelect(model); }}
                        >
                                <span className="video-model-option-head">
                                    <ModelLogo icon={modelIcon(config, model)} size={20} />
                                <strong title={name}>{name}</strong>
                                {selected ? <Check aria-label="当前选中" /> : null}
                                {showPrices ? <span className="video-model-estimate">
                                    {assessment.estimatedCredits === undefined ? "待报价" : `${assessment.estimatedCredits.toLocaleString("zh-CN", { maximumFractionDigits: 6 })} 积分`}
                                </span> : null}
                            </span>
                            <span className="video-model-channel">{channel}</span>
                            <span className="video-model-summary">{assessment.summary.slice(0, 3).join(" · ")}</span>
                            {showAll && assessment.reason ? <span className="video-model-reason">{assessment.reason}</span> : null}
                            {showAll && assessment.status === "needs_input" ? <span className="video-model-reason">补充素材后可生成</span> : null}
                        </button>;
                    })}
                </section> : null;
            })}
        </div>
    </div>;
}

import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { renderToStaticMarkup } from "react-dom/server";
import { ConfigProvider, Select } from "antd";
import { useContext } from "react";
import { CollectionToolbar } from "@/components/layout/collection-toolbar";
import { getWorkspaceAntThemeConfig } from "@/lib/app-theme";
import { assetGridCardMinWidth, assetGridDensityOptions, parseAssetGridDensity } from "@/pages/assets/asset-grid-density";

test("density restores all saved choices and defaults invalid preferences to standard", () => {
    for (const value of [6, 8, 10] as const) {
        expect(parseAssetGridDensity(String(value))).toBe(value);
        expect(parseAssetGridDensity(value)).toBe(value);
    }
    for (const value of [null, undefined, "", "broken", "12"]) {
        expect(parseAssetGridDensity(value)).toBe(8);
    }
});

test("each density has a distinct usable card size at normal library widths", () => {
    expect(assetGridDensityOptions.map(({ value }) => value)).toEqual([6, 8, 10]);
    const widths = [6, 8, 10].map((density) => assetGridCardMinWidth[parseAssetGridDensity(density)]);
    expect(widths).toEqual([272, 208, 160]);
    // CSS auto-fill: n columns fit when n * minimum + (n - 1) * gap <= width.
    for (const available of [720, 900, 1100]) {
        const columns = widths.map((minimum) => Math.floor((available + 16) / (minimum + 16)));
        expect(columns[0]).toBeLessThan(columns[1]);
        expect(columns[1]).toBeLessThan(columns[2]);
    }
});

test("real collection toolbar supplies filled selects without changing their labels or value", () => {
    function PopupConfigProbe() {
        const { select } = useContext(ConfigProvider.ConfigContext);
        return <span>{JSON.stringify(select?.classNames)}</span>;
    }
    const html = renderToStaticMarkup(
        <CollectionToolbar>
            <Select aria-label="素材显示密度" value={10} options={assetGridDensityOptions} />
            <PopupConfigProbe />
        </CollectionToolbar>,
    );
    expect(html).toContain("ant-select-filled");
    expect(html).toContain("素材显示密度");
    expect(html).toContain("紧凑");
    expect(html).toContain('role="combobox"');
    expect(html).toContain("workspace-quiet-popup");
    expect(html).toContain('data-input-modality="keyboard"');
});

test("collection select focus uses a thin keyboard ring, not a persistent pointer outline", () => {
    const css = readFileSync(new URL("../src/styles/workspace-product.css", import.meta.url), "utf8").replaceAll("\r\n", "\n");
    const toolbar = readFileSync(new URL("../src/components/layout/collection-toolbar.tsx", import.meta.url), "utf8");
    expect(css).toContain(".collection-toolbar .ant-select {\n        outline: none !important;");
    expect(css).toContain('[data-input-modality="keyboard"] .ant-select:not(.ant-select-disabled):focus-within');
    expect(css).toContain("outline: 1px solid var(--control-focus-ring) !important;");
    expect(toolbar).toContain('onPointerDownCapture={() => setInputModality("pointer")}');
    expect(toolbar).toContain("onKeyDownCapture=");
    expect(toolbar).toContain("event.currentTarget.contains(event.relatedTarget)");
    expect(toolbar).not.toContain(".blur()");
});

test("workspace menu tokens are shadow-free and use theme-aware surfaces", () => {
    const { components } = getWorkspaceAntThemeConfig();
    for (const name of ["Select", "Dropdown", "Popover"] as const) {
        expect(components?.[name]?.boxShadowSecondary).toBe("none");
        expect(components?.[name]?.colorBgElevated).toBe("var(--user-surface-raised)");
    }
    expect(components?.Select?.optionSelectedBg).toBe("var(--user-surface-hover)");
});

test("real grid consumes density rather than the old fixed column calculation", () => {
    const page = readFileSync(new URL("../src/pages/assets/index.tsx", import.meta.url), "utf8");
    const css = readFileSync(new URL("../src/styles/workspace-product.css", import.meta.url), "utf8");
    const globals = readFileSync(new URL("../src/styles/globals.css", import.meta.url), "utf8");
    expect(page).toContain('"--collection-grid-min-width": `${assetGridCardMinWidth[gridDensity]}px`');
    expect(css).toContain("minmax(min(100%, var(--collection-grid-min-width, 224px)), 1fr)");
    expect(globals).not.toContain("--assets-grid-columns");
    expect(css).not.toContain(".assets-library-page .assets-library-grid { grid-template-columns: repeat(2");
});

test("menu surfaces are explicitly scoped and old account inner overrides are removed", () => {
    const css = readFileSync(new URL("../src/styles/workspace-menus.css", import.meta.url), "utf8");
    // CSS 片段断言必须归一化换行；否则 Windows 检出下同一规则会因 CRLF 假失败。
    const globals = readFileSync(new URL("../src/styles/globals.css", import.meta.url), "utf8").replaceAll("\r\n", "\n");
    expect(css).toContain("@layer utilities");
    expect(css).toContain(".workspace-account-popover .ant-popover-container");
    expect(css).toContain("border: 0 !important");
    expect(css).toContain("box-shadow: none !important");
    expect(css).not.toContain(".ant-modal");
    expect(globals).not.toContain(".workspace-account-popover .ant-popover-inner");
    // 统一控件重构后，旧的选择器片段（:has(input:focus-visible)）已不再存在，
    // 这里改为验证当前真实生效的合同：下拉浮层无描边、键盘焦点只走 focus-visible。
    expect(globals).toContain(":where(.ant-select-dropdown, .ant-dropdown-menu) {\n    border: 0 !important;");
    expect(globals).toContain(".ant-select.app-unified-select[data-input-modality=\"keyboard\"]:not(.ant-select-open)");
    expect(globals).toContain(":where(.ant-select:focus-visible, .ant-tree-select:focus-visible, .ant-cascader-picker:focus-visible, .ant-auto-complete:focus-visible) {");
    expect(globals).not.toContain(".ant-select-focused .ant-select-selector, .ant-select-open .ant-select-selector) {\n    border-color: var(--unified-select-border) !important;\n    box-shadow: none !important;\n    outline: auto");
});

test("shared single-select popup uses a borderless surface instead of a bright focus frame", () => {
    const select = readFileSync(new URL("../src/components/ui/base/select/select.tsx", import.meta.url), "utf8");
    // 统一 Select 现在只挂语义 class，视觉集中在 app-theme.ts 与 globals.css。
    expect(select).toContain('className={cn("app-unified-select"');
    expect(select).toContain('app-unified-select--${appearance}');
    expect(select).toContain('variant={variant ?? "filled"}');
    expect(select).toContain("data-input-modality={inputModality}");
    expect(select).not.toContain('setPopoverWidth(width + 2)');
});

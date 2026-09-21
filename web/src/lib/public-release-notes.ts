const hiddenReleaseNotePatterns = [
    /PR\s*#/i,
    /原作者/,
    /github\.com\//i,
    /ghcr\.io\//i,
    /官方\s*main/i,
    /本地定制/,
    /主分支设计/,
    /项目贡献者/,
];

/**
 * Customer-facing release notes only describe product changes. Repository,
 * contributor and build-registry provenance remains in LICENSE/NOTICE and
 * internal engineering records, not in the product dialog.
 */
export function publicReleaseNotes(source: string) {
    return source
        .replace(/GitHub Release/gi, "发布版本")
        .split(/\r?\n/)
        .filter((line) => !hiddenReleaseNotePatterns.some((pattern) => pattern.test(line)))
        .join("\n")
        .replace(/\n{3,}/g, "\n\n")
        .trim();
}

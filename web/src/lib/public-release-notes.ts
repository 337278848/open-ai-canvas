const hiddenReleaseNotePatterns = [
    /PR\s*#/i,
    /原作者/,
    /作者/,
    /贡献者/,
    /主分支/,
    /上游/,
    /\bfork\b/i,
    /开源/,
    /github\.com\//i,
    /ghcr\.io\//i,
    /github/i,
    /ghcr/i,
    /官方\s*main/i,
    /本地定制/,
    /Host\s*Updater/i,
    /更新组件/,
    /仓库/,
    /发布源/,
];

/**
 * Customer-facing release notes only describe product changes. Repository,
 * contributor and build-registry provenance remains in LICENSE/NOTICE and
 * internal engineering records, not in the product dialog.
 */
export function publicReleaseNotes(source: string) {
    return source
        .split(/\r?\n/)
        .filter((line) => !hiddenReleaseNotePatterns.some((pattern) => pattern.test(line)))
        .join("\n")
        .replace(/\n{3,}/g, "\n\n")
        .trim();
}

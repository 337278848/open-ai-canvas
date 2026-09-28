import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location(
    "check_changelog", Path(__file__).with_name("check-changelog.py")
)
check = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check)


class ChangelogContractTests(unittest.TestCase):
    def test_requires_changelog_for_product_features_and_fixes(self):
        self.assertTrue(check.requires_changelog(["feat(canvas): 新增镜头批量生成"]))
        self.assertTrue(check.requires_changelog(["fix(generation): 修复生成任务超时"]))
        self.assertTrue(check.requires_changelog(["fix: 修复登录失败"]))

    def test_skips_non_product_commit_scopes(self):
        for subject in (
            "fix(ci): 修复发布检查",
            "docs(readme): 更新文档",
            "test(canvas): 补充回归测试",
            "chore(deps): 更新依赖",
            "refactor(worker): 拆分任务执行器",
        ):
            self.assertFalse(check.requires_changelog([subject]), subject)

    def test_accepts_changelog_in_changed_files(self):
        self.assertTrue(check.changelog_changed(["backend/internal/app/task.go", "CHANGELOG.md"]))
        self.assertFalse(check.changelog_changed(["web/src/main.tsx"]))


if __name__ == "__main__":
    unittest.main()

import { expect, test } from "bun:test";

import { parseContributors } from "../src/pages/welcome/contributors-parse";
import { welcomeContributors } from "../src/pages/welcome/contributors";

const identityAvatar = (path: string) => path;

test("welcome contributors stay disabled for the public bundle", () => {
    expect(welcomeContributors).toEqual([]);
});

test("welcome contributors still parse HTML tables without publishing README metadata", () => {
    const markdown = `## 贡献者与团队

<table><tr>
<td><img src="assets/team-member.jpg" alt="团队成员"><strong>团队成员<br>项目发起者 · 微信：team-member</strong><a href="mailto:member%40example.com">联系</a><em>把故事搬上银幕</em></td>
<td><img src="assets/team-qa.jpg" alt="测试成员"><strong>测试成员</strong><a href="mailto:qa@example.com">联系</a><em>认真验证</em></td>
</tr></table>

## 下一章
<table><tr><td><img src="assets/ignored.jpg" alt="不属于本节"></td></tr></table>
`;
    expect(parseContributors(markdown, identityAvatar)).toEqual([
        { name: "团队成员", avatar: "assets/team-member.jpg", signature: "把故事搬上银幕", email: "member@example.com", wechat: "team-member", isFounder: true },
        { name: "测试成员", avatar: "assets/team-qa.jpg", signature: "认真验证", email: "qa@example.com", wechat: undefined, isFounder: false },
    ]);
});

test("welcome contributors still parse markdown pipe tables", () => {
    const markdown = `## 贡献者与团队

| 头像 | 昵称 | 邮箱 | 签名 |
| --- | --- | --- | --- |
| <img src="assets/team-member.jpg" alt="团队成员"> | 团队成员<br>项目发起者 | member@example.com | 把故事搬上银幕 |
| <img src="assets/team-qa.jpg" alt="测试成员"> | 测试成员 | qa@example.com | 把故事搬上银幕 |

## 下一章
`;
    expect(parseContributors(markdown, identityAvatar)).toEqual([
        {
            name: "团队成员",
            avatar: "assets/team-member.jpg",
            signature: "把故事搬上银幕",
            email: "member@example.com",
            wechat: undefined,
            isFounder: true,
        },
        {
            name: "测试成员",
            avatar: "assets/team-qa.jpg",
            signature: "把故事搬上银幕",
            email: "qa@example.com",
            wechat: undefined,
            isFounder: false,
        },
    ]);
});

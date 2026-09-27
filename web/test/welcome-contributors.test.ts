import { expect, test } from "bun:test";

import { parseContributors } from "../src/pages/welcome/contributors-parse";
import { welcomeContributors } from "../src/pages/welcome/contributors";

const identityAvatar = (path: string) => path;

test("welcome contributors stay disabled for the public bundle", () => {
    expect(welcomeContributors).toEqual([]);
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

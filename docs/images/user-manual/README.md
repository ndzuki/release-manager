# user-manual 截图目录

| 目录/前缀 | 内容 | 采集时间与环境 |
| --- | --- | --- |
| `dc-*.png`（37 张） | **当前界面**（方向 C 改版后）：1440×900 整页截图，暗色为主 + 6 张亮色 + 2 张 375px | 2026-10-21，生产 `https://10.10.33.122:8443`（`web:5080198` rev22），真实登录 |
| `legacy/`（22 张） | **改版前**的历史界面，仅作为 `docs/ux-revamp-plan.md` / `docs/ux-review.md` 的历史证据 | 2026-09-28，dev 环境（含当时的登录补丁） |

- 教程 `docs/user-manual.md` **只引用 `dc-*.png`**；看到 `legacy/` 里的图请当作"改版前"，不代表当前产品。
- 文件名编号对应教程章节顺序；长页面（audit / operation detail / values / emergency）是整页截图，高度 > 900。
- 重新采集要求：真实生产会话、`scrollWidth === innerWidth`、暗/亮各一套（见教程 §18）。

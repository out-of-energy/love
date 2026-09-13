# CLI 语法设计：子命令与"任意单词"的冲突

> 触发：`love review` 得到的是单词 `review` 的释义，而不是复习。
> 性质：**语法设计缺陷，不是实现 bug。** `love <word>` 是一个"兜底子命令"（catch-all subcommand）。

---

## 1. 问题

`love` 把任何非 flag 首参都当单词，而规划的子命令 `add` `daily` `review` `stats` `export` 全是真实英文单词：

```
love review  → 查 "review"      love stats  → 查 "stats"
love daily   → 查 "daily"       love export → 查 "export"
```

**每加一个子命令，就多一个查不到的单词。**

---

## 2. 权威指南：明确反对兜底

> **Don't have a catch-all subcommand.** … if the first argument to `mycmd` isn't the name of an
> existing subcommand, you assume the user means `run` … **This has a serious drawback, though: now
> you can never add a subcommand named `echo`—or _anything at all_—without risking breaking existing
> usages.**
> — [clig.dev § Future-proofing](https://clig.dev/#future-proofing)

> Avoid Catch-All Handlers for Unknown Subcommands … **you've accidentally promised to keep that
> behavior working forever.** You've also made error messages ambiguous.
> — [pproenca/dot-skills（引用 clig.dev）](https://github.com/pproenca/dot-skills/blob/HEAD/skills/.experimental/cli-for-agents/references/struct-no-hidden-subcommand-catchall.md)

**真实先例——Deno 主动删掉了自己的兜底：**

> **Remove hack to make "deno $URL" alias to "deno run $URL"**
> — [denoland/deno#4701](https://github.com/denoland/deno/issues/4701)

---

## 3. ⭐ 决定性证据：真实工具怎么处理"首参是任意字符串"

`love` 的困境不是独一无二的。**首参来自开放集合**（词、包名、文件路径、URL）**同时又有多个操作**，这类工具在 Unix 里很多，它们分成两种解法。

### 解法一：**操作做成 flag，首参永远是数据**（推荐）

| 工具 | 它的操作 | 它的首参 |
|---|---|---|
| `tar` | `-c` 创建 `-r` 追加 `-t` 列出 `-x` 解压 | **任意文件路径** |
| `emerge`（Gentoo） | `--sync` `--search` `--unmerge` `--depclean` | **任意包名** |
| `pacman`（Arch） | `-S` 安装 `-R` 卸载 `-Q` 查询 | **任意包名** |
| `curl` | `-O` 下载 `-I` 取头 `-X` 指定方法 | **任意 URL** |
| `wget` | `-r` 递归 `-c` 续传 | **任意 URL** |
| `ffmpeg` | `-i` 输入 `-c` 编解码器 | **任意文件路径** |

本机 `tar --help` 的第一行就是这个设计原则的直接表述：

```
First option must be a mode specifier:
  -c Create  -r Add/Replace  -t List  -u Update  -x Extract
```

**关键性质：`tar nonsense` 永远把 `nonsense` 当文件名，不会因为某天新增了 `-n` 模式而改变含义。** 包名、URL、文件路径和单词一样，都是开放集合——**你不可能穷举它们**。所以操作只能放在不会与数据空间重叠的地方，也就是 flag。

### 解法二：兜底 + 保留字 + 显式逃生舱（`pass`）

`pass` 是最接近的先例：`pass <条目名>` 是默认动作，同时又有子命令。它的 man page SYNOPSIS 写得很清楚：

> **pass** [ _COMMAND_ ] [ _OPTIONS_ ]... [ _ARGS_ ]...
> **If no COMMAND is specified, COMMAND defaults to either `show` or `ls`, depending on the type of
> specifier in ARGS.**
> — [pass(1), Debian manpages](https://manpages.debian.org/testing/pass/pass.1.en.html)

即：**保留字优先，`show` 是逃生舱**（一个叫 `insert` 的条目要用 `pass show insert` 才能看到；`insert` 还"alternatively named **add**"）。

代价是真实存在的——这正是同一个工具上被报告过的故障：

> **[pass] Fwd: Error when naming password GIT**
> — [password-store 邮件列表](https://lists.zx2c4.com/pipermail/password-store/2016-September/002392.html)

**一个叫 `git` 的条目就撞上了子命令。** 对密码管理器这只是偶发麻烦；**对词典工具则是核心功能受损**——因为"词"就是这个工具的数据本身，而 `review`/`daily`/`stats`/`export` 都是常见词。

---

## 4. 三类形态总表

| 形态 | 工具 | 首参含义 | 冲突可能 |
|---|---|---|---|
| ① 纯子命令，无兜底 | `git` `cargo` `gh` `deno` `brew` | 永远是子命令 | ❌ 数据必须挂在子命令下 |
| ② 纯单职，无子命令 | `rg` `fd` `bat` `jq` `vim` | 永远是数据 | ❌ 无法产生 |
| ③ **操作是 flag** | `tar` `emerge` `pacman` `curl` `wget` | **永远是数据** | ❌ **无法产生** |
| ④ 子命令 + 兜底 | `pass`，以及现在的 `love` | 看情况 | ⚠️ **会撞** |

**④ 是唯一会产生冲突的形态。** ① 和 ③ 都靠"结构上不可能重叠"来消除问题，区别只是主操作是子命令还是位置参数。

---

## 5. 我的更正与推荐

### 我此前说过两次错话

1. 第一次我说你的 `love --review` "不合惯例"。**错了**——`tar` / `emerge` / `pacman` / `curl` 全是这个形态，而且它是**开放集合数据**场景下唯一零边界的解法。
2. 第二次我推荐"拆成两个命令"。**也错了**——正如你指出的，没人这么干；而且趋势是相反方向：Docker Compose 并入了 `docker compose`，`ip` 并掉了 net-tools。Unix 的"一个工具一个职责"讲的是**程序之间用管道组合**，不是把同一项目拆成两个二进制。

### 推荐：**方案 B —— `love` 保持单职，引擎操作做成 flag**

> **已决定并实施（2026-09-13）**：采用方案 B。动作注册表在 `main.go` 的 `actions`，
> 守卫测试在 `main_test.go`（动作可达、flag 必以 `-` 开头、flag 与名字唯一、命令名词仍走查词分支）。

```
love maintain         # 查词：首参永远是词，任何词都能查
love --review         # 复习
love --daily          # 生成今日任务 / 发邮件
love --stats          # 学习统计
love --export out/    # 导出备份
```

**理由：**

1. **它保护了 `love` 的核心承诺**："我打任何词，它都告诉我意思。" 单词是开放集合，`love` 不该有一份"不能查的词"名单。`pass` 的教训说明：在**词就是数据**的工具里放保留字，代价比在密码管理器里高得多。
2. **与 `tar` / `emerge` / `pacman` 同构**——这是 Unix 处理"开放集合首参 + 多操作"的标准解法。
3. **无破坏性变更**：`love <word>` 一字不改，已发布的 v0.1/v0.2 行为完全保留；用户不用迁移肌肉记忆。
4. **零边界**：不需要保留字表、不需要逃生舱语法、不需要"冲突时怎么办"的规则。**结构上不可能出错，胜过靠规则避免出错。**

**代价（诚实说明）**：引擎动作写成 `--flag` 略不如 `love review` 自然；`love --help` 需要把"动作"与"选项"分两组列（`tar --help` 就是这么做的）。

### 备选：方案 A（`love review` + `love show review`）

如果你更看重 `love review` 的书写手感，`pass` 证明了这条路可行。但需要接受：

- 至少 5 个常见词（`add` `daily` `review` `stats` `export`）不能再直接查
- 必须实现并文档化 `love show <word>` 逃生舱
- 每次新增子命令都要检查是否吃掉了一个常用词——**这正是 clig.dev 警告的"永久承诺"**

---

## 6. 无论选哪个都要做的两件事

### ① 唯一的动作注册表 + 防回归测试

把动作名放进**一份表**，并加测试断言：

> **凡在表里的名字，都不允许落进单词查询分支；反之，任何单词都不允许被动作分支吞掉。**

这样以后每加一个动作，测试会强制你面对冲突——而不是等用户在终端里踩到。

### ② 边界情况必须明确，不能靠猜

选 B 之后不存在"歧义"——两条语法不相交。实际实现的三种边界，都由解析器直接拒绝而不是猜：

```
$ love review                 # 合法：就是一个单词
review /rɪˈvjuː/
ELI5: To look at something again.

$ love --review maintain      # 两条语法同时出现
love: --review 不接受单词参数

$ love --nope                 # 未知 flag，列出可用项
love: unknown option "--nope"
可用的动作:
  --review     开始今天的复习
  --daily      生成今天的邮件并发送
```

来自 clig.dev：**"The user is conversing with your software… At worst, it's a hostile conversation
which makes them feel stupid and resentful."** 静默猜一个，就是 hostile 的那种——而 `love review`
当初静默地当成查词，正是这个问题。

---

## 7. 事故现场

```
$ love review
review /rɪˈvjuː/

ELI5: When you look at something again to see if it is good or bad.
中文：复习
```

`review` 已被写进词库第 22 行（`created_at: 2026-09-13T17:08:52+08:00`）。
这正是 clig.dev 警告的"兜底让错误输入产生意外副作用"：想要复习，得到一次付费查询，还污染了词库。

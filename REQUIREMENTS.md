# 命令行单词词典 CLI (`ewh`) — 需求说明书 v0.2

> 原始想法：*"命令行单词词典。需要 DeepSeek token key 放在系统 OS 环境变量，默认用最快最便宜的模型。"*
> 本文把这句话展开成可执行、可验收的工程需求。

> ⚠️ **这是最初的词典 CLI 需求书，已被后续规划取代。**
> 项目后来从"词典"长成"记忆引擎"，当前设计与里程碑在
> [`docs/v0.3-review.md`](docs/v0.3-review.md)；命令语法见 [`docs/cli-design.md`](docs/cli-design.md)；
> 两层内容模型见 [`docs/content-layers.md`](docs/content-layers.md)。
>
> 本文中仍然有效的部分：查词流程、词库格式与去重、退出码、测试方法。
> **已失效的部分**：§8 里程碑与 §11 之后的 M2/M3 计划（`--json`、`show`、`list`、`stats`、
> `path`、`config`、`--refresh`、`--no-cache`、`import`、`rm`、`export` 这一串）。
> 命令名也从 `ewh` 定为 `love`。历史保留，便于回看当时的取舍。

---

## 1. 背景与目标

现在已经有一个 prompt 形式的 skill（`english-word-helper`）：靠模型每次现场执行"查 `words.jsonl` → 命中返回 / 未命中生成 → 追加"。

它有两个结构性问题：

1. **不是真正的程序**。必须打开 agent 会话才能用，终端里没法直接查。
2. **缓存写不进去**。缓存文件在 `~/.dsh/skills/english-word-helper/words.jsonl`，落在工作区沙箱之外，未命中时追加会被拒绝，"查一次永久复用"的承诺静默失效。

因此目标是把这套逻辑固化成**一个本地可执行程序**：

| 目标 | 说明 |
|---|---|
| 真 CLI | 终端一条命令查词，不依赖 agent |
| 缓存优先 | 命中缓存 = 0 成本、0 网络、毫秒级返回 |
| 按需付费 | 只有未命中才调用 DeepSeek，且默认用最便宜最快的模型 |
| 数据自持 | 词库是本地纯文本 JSONL，用户可读、可备份、可手改 |
| 格式稳定 | 输出格式与现有 skill 保持一致，学习体验不变 |

**一句话定位**：一个会自己攒词库的、按次付费的终端英汉词典。

---

## 2. 术语

- **词条 (record)**：`{word, ipa, eli5, chinese}` 四个字段，一个词一条。
- **缓存 (cache)**：JSONL 文件，一行一个词条 JSON。
- **命中 / 未命中**：缓存里是否有该词（规范化后精确匹配）。

---

## 3. 用户故事

- 作为英语学习者，我在终端敲 `ewh serendipity`，希望立刻看到 **音标 / 三岁小孩能懂的英文解释 / 中文含义**。
- 作为常客，我查过的词第二次查要**瞬间返回且不花钱**。
- 作为使用者，我想知道**词库在哪个文件**、攒了多少词，并能在换电脑时把它带走。
- 作为开发者，我不想为了查个词去开一次 AI 会话。

---

## 4. 功能需求

### 4.1 核心命令

**M1（MVP）必须实现**

```bash
ewh <word>          # 查词：缓存优先，未命中调 API
ewh --help
ewh --version
```

**M2 实现**

```bash
ewh <word> --json        # 机器可读输出（单行 JSON）
ewh show <word>          # 只读缓存，绝不联网；未命中则报"未收录"
ewh --refresh <word>     # 忽略缓存，重新生成并覆盖该词条
ewh --no-cache <word>    # 只查不写，不污染词库
ewh list                 # 列出词库（--limit N / --json）
ewh stats                # 词条数、文件路径、文件大小
ewh path                 # 打印缓存文件绝对路径
ewh config               # 打印生效配置（API key 只显示是否已设置）
```

**M3 实现**（可选）

```bash
ewh rm <word>            # 删除某词条
ewh import <file>        # 导入外部 JSONL（含旧 skill 词库），自动去重
ewh export --format csv|anki
```

### 4.2 查词流程（cache-first，不可颠倒）

```
输入 → 规范化 → 查缓存
                 ├─ 命中 → 直接输出（0 次网络请求）
                 └─ 未命中 → 调 DeepSeek → 校验 → 追加落盘 → 输出
```

**规范化规则**
- 去除首尾空白
- 转小写（`BOOK` → `book`）
- 保留词内连字符与空格（`ice cream`、`well-known`）
- 输入为空或明显不是英文词 → 用法错误提示，退出码 2

**命中时**
- 直接使用已有记录，**不重新生成、不修改、不重复追加**
- 同一 word 存在多条 → 用**第一条**，不再新增

**未命中时**
- 调用模型生成三个字段
- 校验：三字段非空、IPA 形如 `/.../`、中文非空
- 校验通过 → 追加落盘 → 输出
- **落盘失败仍要输出结果**，但 stderr 打印警告且退出码非 0（避免"假装已保存"）

### 4.3 输出格式（与现有 skill 一致）

默认人读格式，**必须**逐字沿用：

```
serendipity /ˌserənˈdɪpəti/

ELI5: A happy thing you find by chance.

中文：意外发现美好事物
```

- TTY 下可加颜色（词头加粗）；管道/重定向时纯文本
- 只输出以上内容。**不**输出 JSON、文件路径、"已保存"之类噪声
- `--json` 时输出单行 JSON（与缓存同构）：

```json
{"word":"serendipity","ipa":"/ˌserənˈdɪpəti/","eli5":"A happy thing you find by chance.","chinese":"意外发现美好事物"}
```

### 4.4 缓存

**路径解析优先级**

```
--cache <path>  >  $EWH_CACHE  >  默认路径
```

默认路径（待确认，见 §10）：`~/.ewh/words.jsonl`

**文件格式**
- UTF-8、LF 换行、一行一个合法 JSON 对象、无 BOM
- 只允许 `word` / `ipa` / `eli5` / `chinese` 四个字段，**不新增字段**
- 文件不存在时自动创建（含父目录）

**并发与原子性**
- 追加使用 `O_APPEND` 单次 `write`（≤ PIPE_BUF 级别），保证行不撕裂
- 进程间用文件锁（`flock` / `LockFileEx`）串行化"读-查-写"，避免并发重复生成
- 绝不为查一个词重写整个文件

**读取容错**（缓存是用户可手改的，必须宽容）
- 跳过空行
- 非法 JSON 行：跳过，stderr 警告，不影响其他词条
- 字段缺失的词条：视为无效并跳过

**数据安全**
- 除 `--refresh` / `rm` 显式要求外，**不删改历史记录**
- `--refresh` 覆盖时保留同一行位置或追加新行并去重（实现时统一为"重写该文件一次"）

### 4.5 模型调用

| 项 | 要求 |
|---|---|
| API Key | **只**从环境变量 `DEEPSEEK_API_KEY` 读取 |
| Key 安全 | 绝不写入磁盘、日志、错误信息、`--json` 输出、`config` 输出 |
| Key 缺失 | 立即报错并给出设置示例，退出码 2，不发起请求 |
| Base URL | 默认 `https://api.deepseek.com/v1`，可被 `DEEPSEEK_BASE_URL` 覆盖（便于代理） |
| 模型 | 默认 **`deepseek-v4-flash`**（最便宜最快），可被 `--model` / `EWH_MODEL` 覆盖 |
| 思考模式 | 关闭思考（non-thinking），纯查词不需要推理链，省钱省时 |
| 输出约束 | 强制 JSON 输出（`response_format: json_object`）+ 提示词内明确 JSON 结构 |
| temperature | 低（约 0.3），保证同一词结果稳定 |
| 超时 | 15s |
| 重试 | 仅对网络错误 / 429 / 5xx 重试 1 次，指数退避；4xx（除 429）不重试 |
| 解析失败 | 重试 1 次；仍失败则报错，**且不写入缓存** |

> 文档口径差异需实测校准：官方 API 参考写 `thinking: {type: enabled|disabled}` + `reasoning_effort`，第三方指南写 `thinking_mode: non-thinking`。实现时以真实 API 返回为准，并保留开关。

### 4.6 退出码

| 码 | 含义 |
|---|---|
| 0 | 成功 |
| 1 | 通用错误 |
| 2 | 用法 / 配置错误（缺 key、参数错、空输入） |
| 3 | 网络 / API 错误 |
| 4 | 缓存写入失败（结果已输出） |

---

## 5. 非功能需求

- **单二进制**：`go build` 产出无运行时依赖的可执行文件；跨 macOS / Linux / Windows
- **零第三方依赖优先**：仅用 Go 标准库（`net/http`、`encoding/json`、`os`）即可，便于审计与编译
- **快**：缓存命中冷启动 < 50ms；查词不做任何网络预检
- **可测**：单元测试覆盖规范化、命中/未命中、JSONL 容错、重复词、并发追加；API 层用 `httptest` 假服务器测试，**测试不消耗真实额度**
- **可观测**：`--verbose` 时向 stderr 打印是否命中缓存、耗时、token 用量（不含 key）
- **不做后台服务**：无 daemon、无遥测、无自动更新

---

## 6. 配置总览

| 配置 | 优先级（高 → 低） | 默认值 |
|---|---|---|
| API Key | `DEEPSEEK_API_KEY`（唯一来源） | 无，缺失即报错 |
| Base URL | `--base-url` > `DEEPSEEK_BASE_URL` | `https://api.deepseek.com/v1` |
| 模型 | `--model` > `EWH_MODEL` | `deepseek-v4-flash` |
| 缓存路径 | `--cache` > `EWH_CACHE` | `~/.ewh/words.jsonl` |
| 超时 | `--timeout` | `15s` |

---

## 7. 项目结构

```
english-word-help-cli/
├── go.mod
├── main.go                  # 命令解析与装配
├── internal/
│   ├── cache/               # JSONL 读写、文件锁、规范化查找、导入导出
│   ├── dict/                # DeepSeek 客户端、提示词、响应校验
│   └── render/              # 人读 / JSON / 颜色输出
├── testdata/                # 样例 JSONL（含脏数据用例）
├── REQUIREMENTS.md          # 本文
└── README.md                # 安装与用法
```

---

## 8. 里程碑

| 阶段 | 交付 | 验收标准 |
|---|---|---|
| **M1 MVP** | `ewh <word>` 全链路 | 未命中能联网生成并落盘；再次查询 0 网络、毫秒返回；格式与 skill 一致 |
| **M2 可用** | `--json` `show` `list` `stats` `path` `config` `--refresh` `--no-cache` | 脚本可稳定消费 `--json`；`show` 确认不发请求 |
| **M3 迁移** | `import` `rm` `export` 颜色、shell 补全 | 现有 8 个词一键接管，去重正确 |
| **M4 增强** | 批量查词、多词义、TTS 发音 | 按需 |

---

## 9. 明确不做（Non-goals）

- ❌ GUI / Web 界面
- ❌ 云同步、账号体系、`ewh login`
- ❌ 保存或代管 API key（只读环境变量）
- ❌ 完整词典（多词义、例句、词源、语法分析）—— 坚持 **one word, one meaning**
- ❌ 后台常驻进程 / 遥测 / 自动更新

---

## 10. 已确认决策（2026-09-13）

| # | 问题 | 决策 |
|---|---|---|
| 1 | 命令名 | **`love`**，用法 `love <word>` |
| 2 | 缓存默认路径 | **`~/.ewh/words.jsonl`**（新建，保证可写）；旧 skill 的 8 个词通过 `import` 接管（M3） |
| 3 | 首版范围 | **只做 M1 MVP**：`love <word>` + `--help` + `--version`，最快跑通全链路 |
| 4 | 输入粒度 | **单词与短语都支持**（`love ice cream` / `love "ice cream"` 均可用） |
| 5 | 落盘失败策略 | 仍输出结果 + stderr 警告 + 退出码 4 |
| 6 | 联调 Key | 用户自行 `export DEEPSEEK_API_KEY`；单元测试用 `httptest` 假服务器，不消耗额度；随后做一次真实端到端验证 |

> 命名提示：`love` 与 LÖVE (love2d) 游戏引擎的可执行文件同名。若将来冲突，可改为 `luv` 或加 shell 别名。

**M1 交付清单**

```bash
love <word>          # 查词：缓存优先，未命中调用 DeepSeek
love --help
love --version
```

MVP 阶段的实现**已包含**：规范化（含短语）、JSONL 容错读取、首条命中优先、加锁去重追加、缺 key 报错、网络/429/5xx 重试一次、解析失败重试一次。
以下留到 M2：`--json`、`show`、`list`、`stats`、`path`、`config`、`--refresh`、`--no-cache`。

---

## 11. 实现状态（2026-09-13）

| 项 | 状态 |
|---|---|
| Go 模块与四个包（`main` / `cache` / `dict` / `render`） | ✅ 完成 |
| 单元测试（cache / dict / render / main） | ✅ 全绿，`-race` 通过 |
| 覆盖率 | main 65%、cache 75%、dict 87%、render 50% |
| 端到端（本地 mock API 驱动真实二进制，19 项断言） | ✅ 全部通过 |
| 二进制安装 | ✅ `/opt/homebrew/bin/love` (v0.1.0) |
| 旧 skill 词库迁移 | ✅ 8 个词 → `~/.ewh/words.jsonl`，去重后 8 条 |
| **真实 DeepSeek API 验证** | ✅ 完成（2026-09-13） |

**真实 API 校准结果**

```console
$ love curious            # 首次：联网生成并落盘
curious /ˈkjʊəriəs/

ELI5: You want to know about things. You ask lots of questions.

中文：好奇的

$ love CURIOUS            # 再次：0.009s，无网络请求
（同样输出）
```

直接探测 `POST /v1/chat/completions` 的状态码：

| 请求 | 结果 |
|---|---|
| `model: deepseek-v4-flash` + `thinking: {"type":"disabled"}` | **HTTP 200** |
| `model: deepseek-v4-flash`，不带 `thinking` | HTTP 200 |

结论：

1. 模型 id `deepseek-v4-flash` **有效**。
2. `thinking: {"type":"disabled"}` **被接受**，无需改用第三方文档提到的 `thinking_mode`；代码里的 400 回退分支属于保险，正常路径不触发。
3. 缓存命中实测 **9ms**，且在完全无 `DEEPSEEK_API_KEY` 的环境下正常返回 —— 命中路径确实零网络。

### 怎么测试

| 层级 | 命令 | 需要 key | 花费 | 覆盖范围 |
|---|---|---|---|---|
| 1. 单元测试 | `go test ./...` | ❌ | 0 | 规范化、容错读取、首条命中、去重追加、HTTP 解析、重试与回退、渲染格式 |
| 2. 竞态测试 | `go test -race -count=1 ./...` | ❌ | 0 | 加锁追加的并发安全 |
| 3. 端到端（mock） | `python3 scripts/e2e_mock_check.py` | ❌ | 0 | 真实二进制 + 真实 HTTP + 落盘 + 退出码，19 项断言 |
| 4. 真实 API | `love <新词>` 然后 `love <同一个词>` | ✅ | 约 ¥0.0001/词 | 真实模型、真实账单、真实落盘 |
| 5. 手动冒烟 | 见下方断言清单 | 部分 | 0 | 退出码、容错、短语、大小写、管道输出 |

**手动冒烟断言清单**

```bash
love evil                 # 命中：无需 key，exit 0
love SIGN                 # 大小写不敏感，exit 0
love "  symlink "         # 短语/空白规范化，exit 0
love --nope               # 未知参数，exit 2
env -u DEEPSEEK_API_KEY love serendipity   # 未命中且无 key，exit 2，stdout 为空
love curious | cat        # 管道下无颜色转义
NO_COLOR=1 love evil      # 强制关闭颜色
```

**回归防线**：第 1~3 层全部不需要 key，适合放进 CI 或 git pre-push；第 4 层只在人工确认时跑一次。

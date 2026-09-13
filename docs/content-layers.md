# 内容分层设计：锚点 + 扩展

> 决策来源：用户 2026-09-13
> 修正：v0.3 评估中我曾建议"取消 AI 的 `meaning` 字段、只保留 `eli5`"，理由是两者语义重叠。
> **该建议错误。** 我只对比了字段名，没有区分它们在**什么时候被读**、服务于**哪种能力**。

---

## 1. 为什么是两层而不是一层

一个词要经历两个不同阶段：

```
认得（recognize）  →  会用（produce）
     ↑                    ↑
  第一部分              第二部分
  快速唤起              扩展活用
```

- **第一部分**回答"这个词是什么"。要求**快**、**稳**、**一眼就懂**。任何需要阅读理解的解释都会拖慢复习节奏。
- **第二部分**回答"这个词怎么用"。要求**有语境**、**能模仿**。单纯的释义无法让人学会主动使用一个词。

两层缺一不可：只有第一部分 → 永远停在"认得但不会用"；只有第二部分 → 每次都像在读新课文，没有记忆锚点。

---

## 2. 第一部分：锚点层

| 字段 | 来源 | 是否会被 AI 重写 |
|---|---|---|
| `ipa` | `words.jsonl` | ❌ 永不变 |
| `eli5` | `words.jsonl` | ❌ 永不变 |
| `chinese` | `words.jsonl` | ❌ 永不变 |

**关键约束：锚点层永不重新生成。**

同一个词每次复习看到完全相同的 ELI5 和音标——记忆依赖**稳定的检索线索**。如果每次换个说法，复习就变成了"读一段新文字"，而不是"回忆一个已知的东西"。

这也是为什么锚点层放在 `words.jsonl`（用户资产）而不是 `generated.jsonl`（可重建缓存）。

---

## 3. 第二部分：扩展层

| 字段 | 说明 |
|---|---|
| `meaning` | 比 ELI5 更进一步的自然释义（ELI5 是"三岁小孩能懂"，这里是"成年人正常说法"） |
| `examples[]` | 1–3 个例句 |
| `scene` | 一句话场景，说明这些句子发生在什么情境 |
| `dialogue[]` | **2–4 轮短对话**，必须真的用到这个词 |

### 关于 `meaning` 与 `eli5` 的关系

它们**不是重复，是递进**：

```
eli5    : To keep something working well.        ← 三岁小孩能懂
meaning : To keep something in good condition,   ← 成年人正常说法
          or to continue something over time.
```

先给最简单的，再给更完整的。这是**渐进披露**（progressive disclosure），符合认知顺序。
所以两者都保留——这正是用户指出的那点。

### `dialogue` 是 v0.3 契约里缺的

v0.3 §14 的 AI 契约只有 `example` 和 `scene`，没有对话。而"活用"最有效的形式恰好是对话——它给出**你来我往的真实语境**，且更接近实际开口的场景。

---

## 4. 更新的 AI 内容契约

```json
{
  "word": "maintain",
  "meaning": "to keep something in good condition, or to continue it over time",
  "examples": [
    "I maintain my bicycle every month.",
    "She maintains a small garden behind the house."
  ],
  "scene": "Someone taking care of the things they own, so they last longer.",
  "dialogue": [
    {"speaker": "A", "line": "Your bike still looks new."},
    {"speaker": "B", "line": "I maintain it every month."},
    {"speaker": "A", "line": "That explains it. Mine is falling apart."}
  ]
}
```

**约束**
- `dialogue` 至少一处**必须**真实使用目标词（不能只出现在例句里）
- 全部内容按 `word_id` 缓存进 `generated.jsonl`，**一次生成、永久复用**
- AI 失败时降级：只发第一部分（锚点层永远可用，因为它在 `words.jsonl` 里）

---

## 5. 邮件结构

```html
┌─────────────────────────────────────┐
│  第一部分 · 先看这个                  │
│                                     │
│  maintain  /meɪnˈteɪn/              │
│                                     │
│  ELI5: To keep something working    │
│        well.                        │
│                                     │
│  中文：维护，保持                     │
└─────────────────────────────────────┘

┌─────────────────────────────────────┐
│  第二部分 · 用起来                    │
│                                     │
│  释义                                │
│  to keep something in good          │
│  condition, or to continue it       │
│  over time                          │
│                                     │
│  例句                                │
│  · I maintain my bicycle every      │
│    month.                           │
│  · She maintains a small garden     │
│    behind the house.                │
│                                     │
│  对话                                │
│  A: Your bike still looks new.      │
│  B: I maintain it every month.      │
│  A: That explains it.               │
└─────────────────────────────────────┘
```

**渲染约束**（邮件客户端会剥离 `<head>` 里的样式）：内联样式 + table 布局，并附纯文本版本。

---

## 6. 对复习 CLI（M3）的影响

终端复习同样遵循"先认得、后活用"：

```
$ love --review

1 / 5

  maintain
  记得它的意思吗？

  [回车揭示]

  ── 第一部分 ──────────────────
  maintain  /meɪnˈteɪn/
  ELI5: To keep something working well.
  中文：维护，保持

  ── 第二部分 ──────────────────  [按 e 展开]
  · I maintain my bicycle every month.
  A  Your bike still looks new.
  B  I maintain it every month.

  Rating:  1 Again   2 Hard   3 Good   4 Easy
  >
```

- **揭示时先只给第一部分**——这是回忆的答案，给多了就不是回忆了
- **第二部分按 `e` 展开**，默认折叠：复习要快，扩展是可选动作
- 评分后再看第二部分也可以，但默认放在评分前、按需展开更自然

---

## 7. 数据落位

| 数据 | 文件 | 是否可重建 |
|---|---|---|
| 锚点层（ipa / eli5 / chinese） | `words.jsonl` | ❌ 用户资产，丢失需重新查询 |
| 扩展层（meaning / examples / scene / dialogue） | `generated.jsonl` | ✅ AI 可重新生成 |
| 复习事件 | `reviews.jsonl` | ❌ 唯一历史 |
| 派生状态 | `memory.json` | ✅ 可从事件重放 |

这个划分的意义：**锚点层是资产，扩展层是缓存**。删掉 `generated.jsonl` 只损失一次 API 花费，不影响记忆历史；而 `eli5` 必须跟着词条一起被备份。

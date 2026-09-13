# love

A cache-first command line English dictionary.

```console
$ love serendipity
serendipity /ˌserənˈdɪpəti/

ELI5: A happy thing you find by chance.

中文：意外发现美好事物
```

The first lookup for a word asks DeepSeek for its IPA, an explanation simple
enough for a three-year-old, and one common Chinese meaning. The answer is
stored in your own word file. **Every lookup after that is served from that
file with no network request and no cost.**

## Why

A word you already looked up should never cost money or time again. The word
file is plain JSONL under your home directory, so it is readable, greppable,
backup-able and hand-editable — it belongs to you, not to the tool.

## Install

Requires Go 1.26+.

```bash
go install github.com/out-of-energy/love@latest   # or:
go build -trimpath -ldflags "-s -w" -o /opt/homebrew/bin/love .
```

## Setup

The API key is read from the environment only. It is never written to disk,
never logged, and never printed.

```bash
export DEEPSEEK_API_KEY="sk-..."
```

Add that line to `~/.zshrc` or `~/.bashrc` to make it permanent.

## Usage

```bash
love <word>          # look a word up
love --review        # work through today's review
love --daily         # build and send today's email
love --help
love --version
```

Phrases work too, quoted or not:

```bash
love ice cream
love "ice cream"
```

Input is normalized before lookup — trimmed, lowercased, and internal spacing
collapsed — so `love EVIL` finds the entry stored for `evil`.

### Why actions are flags

`love <word>` accepts any word at all, and words are an open set that cannot be
enumerated. A subcommand named `review` would make that word permanently
unlookupable, and every future subcommand would consume another word — which is
exactly what happened: `love review` used to look up the word "review", write it
to the word file, and cost an API call.

So the two grammars are kept disjoint. `love maintain` is always a lookup;
`love --review` is always the action; `love --review maintain` is a usage error
rather than a guess. The same shape is what `tar` (`-c`/`-t`/`-x`), `emerge`,
and `pacman` (`-S`/`-R`/`-Q`) use, for the same reason. See
`docs/cli-design.md` for the research behind the decision.

### Daily options

```bash
love --daily --dry-run              # render the digest, send nothing
love --daily --out /tmp/today.html  # also write the HTML
```

## Output

Exactly three lines, always:

```text
word /ipa/

ELI5: <a very simple English explanation>

中文：<one common Chinese meaning>
```

Nothing else is printed on success: no JSON, no file paths, no "saved"
messages. On a terminal the head line is bold; when piped or when `NO_COLOR` is
set, output is plain text.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `DEEPSEEK_API_KEY` | — | **Required.** Your DeepSeek API key. |
| `DEEPSEEK_BASE_URL` | `https://api.deepseek.com/v1` | API base URL. |
| `EWH_DIR` | `~/.ewh` | Directory holding the store. |
| `EWH_CACHE` | — | Words file path, kept for backwards compatibility. |
| `EWH_MODEL` | `deepseek-v4-flash` | Model id. |
| `NO_COLOR` | — | Disable colored output when set. |

The default model is the fastest and cheapest one, because a single word lookup
does not need more. Reasoning is switched off for the same reason: reasoning
tokens would only be discarded.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Unexpected error |
| 2 | Usage or configuration error (no word, unknown flag, missing key) |
| 3 | Network or API error |
| 4 | The answer was printed but could not be cached |

## The word file

One JSON object per line, eight fields, nothing else:

```json
{"id":"4adb2c8c","word":"evil","normalized":"evil","ipa":"/ˈiːvəl/","eli5":"Very, very bad.","chinese":"邪恶的","source":"cli","created_at":"2026-09-13T14:47:22+08:00"}
```

The store is four files under the store directory:

| File | Role | Rebuildable |
|---|---|---|
| `words.jsonl` | your words | ❌ an asset |
| `reviews.jsonl` | your learning history, append-only | ❌ the only history |
| `memory.json` | derived scheduling state | ✅ from the two above |
| `generated.jsonl` | cached example sentences and dialogue | ✅ costs one API call |

The split matters. `words.jsonl` holds the anchor layer — IPA, the child-simple
explanation, one Chinese meaning — and it is never regenerated, because memory
depends on a stable cue. `generated.jsonl` holds the expansion layer, which is
only practice material and is free to be rebuilt.

`id` and `normalized` exist so a word has a stable identity and a single
comparison key. `normalized` is stored rather than recomputed, so a future
change to the normalization rule cannot silently re-partition existing data.

- A cache **hit** never writes to the file, so queries do not churn it.
- A cache **miss** appends exactly one line, under a lock, and re-checks for a
  duplicate first. Two simultaneous lookups cannot produce two records, and
  adding the same word twice is idempotent.
- Damaged lines are **skipped with a warning**, never fatal — one bad line
  cannot hide the rest of your dictionary.
- Duplicate entries resolve to the **first** record, so an existing file with
  duplicates keeps working.
- The tool never rewrites history to "fix" the file on its own.

### Upgrading an older word file

A file written by an earlier version (four fields, no `id`) is upgraded
automatically the first time you run `love`:

```console
$ love evil
love: upgraded 21 word(s) to the current format (backup: /Users/you/.ewh/words.jsonl.bak-20260913-150150)
evil /ˈiːvəl/
...
```

Every original field is preserved byte for byte, the file order is kept, and a
backup is written first. If the file contains a damaged line the upgrade is
**refused** rather than silently dropping it — fix the line and run again.

## Testing

Four layers, cheapest first. The first three need **no API key** and cost
nothing, so they are the ones to run on every change.

| Layer | Command | Key | Cost | Covers |
|---|---|---|---|---|
| 1. Unit | `go test ./...` | no | free | normalization, tolerant reading, first-record-wins, dedup append, HTTP parsing, retry and fallback, render format |
| 2. Race | `go test -race -count=1 ./...` | no | free | concurrent appends under the write lock |
| 3. End-to-end, mocked | `python3 scripts/e2e_mock_check.py` | no | free | the real binary over real HTTP: generate → persist → hit, plus every exit code |
| 4. Real API | `love <new-word>` then `love <same-word>` | yes | ~1 call | the real model, real billing, real persistence |

`scripts/e2e_mock_check.py` spawns a throwaway local HTTP server and drives the
compiled binary against it. It asserts 19 conditions, including that the second
lookup makes **zero** requests and that failures never write to the word file.
Set `LOVE_BIN=/path/to/love` to test a binary that is not on `PATH`.

### Manual smoke checklist

```bash
love evil                                   # cache hit, no key needed  -> 0
love SIGN                                   # case-insensitive           -> 0
love "  symlink "                           # trimming and phrases       -> 0
love --nope                                 # unknown flag               -> 2
love                                        # no word                    -> 2
env -u DEEPSEEK_API_KEY love serendipity    # miss without a key         -> 2, empty stdout
love curious | cat                          # piped output has no escapes
```

### Checking color by hand

`love` colors output only when stdout is a terminal and `NO_COLOR` is unset.
Many CI shells and editors export `NO_COLOR=1`, which correctly turns color
off — do not mistake that for a bug.

```bash
love evil | cat -v                      # piped: no escape codes
script -q /dev/null love evil | cat -v  # fake a tty: expect ^[[1m ... ^[[0m
NO_COLOR=1 love evil                    # forced off
```

### Verifying the real API path

```bash
export DEEPSEEK_API_KEY="sk-..."
love curious        # first call hits the network and appends one line
love curious        # second call must return instantly with no request
```

Confirm the second call is genuinely offline by running it with no key present
at all — a cache hit must still succeed:

```bash
env -u DEEPSEEK_API_KEY love curious
```

## Roadmap

The project grew from a dictionary into a memory engine, so the plan lives in
`docs/` rather than in the original `REQUIREMENTS.md`. Status:

| Milestone | What | State |
|---|---|---|
| M1 | the store: four files, atomic writes, migration from the four-field format | ✅ |
| M2 | the scheduler: Leitner boxes, the daily budget, a fake clock | ✅ |
| M3 | `--review`: recall first, anchor as the answer, expansion behind `e` | ✅ |
| M4 | the expansion layer: generated, validated, cached | ✅ |
| M5 | `--daily`: generate, render, send | ✅ |
| | `--stats`, `--export` | planned |
| | a Reminders adapter, and a launchd job to run `--daily` each morning | planned |

`docs/v0.3-review.md` holds the current design review and the milestone detail;
`docs/cli-design.md` explains the command grammar; `docs/content-layers.md`
explains why there are two layers.

## Note on the name

`love` collides with the LÖVE (love2d) game engine's executable. If you have
that installed, use an explicit path or a shell alias.

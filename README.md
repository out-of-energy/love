# love

A cache-first command line English dictionary.

```console
$ love serendipity
serendipity /ˌserənˈdɪpəti/
Phonics: ser·en·dip·i·ty → /ˌser/ · /ən/ · /ˈdɪp/ · /ə/ · /ti/
Parts: Serendip (old name of Sri Lanka) · -ity (state of) ⇒ "the state of Serendip"
ELI5: A happy thing you find by chance.
```

The first lookup for a word asks DeepSeek for its IPA, how to sound it out, how
it is built from a prefix, a root and a suffix, an explanation simple enough for
a three-year-old, and one common Chinese meaning. The answer is stored in your
own word file. **Every lookup after that is served from that file with no
network request and no cost.**

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

`@latest` resolves to the newest **tagged** release. Check which one you got
with `love --version`, and use `@main` if you want the tip of the branch rather
than the last release.

## Setup

The API key is read from the environment, or from a file. It is never written
to the store, never logged, and never printed.

```bash
export DEEPSEEK_API_KEY="sk-..."          # simplest

# or, preferred once the daily run is scheduled: a launch agent inherits no
# shell profile, so a scheduled run needs a file
printf '%s' "sk-..." > ~/.ewh/deepseek-key && chmod 600 ~/.ewh/deepseek-key
export DEEPSEEK_API_KEY_FILE=~/.ewh/deepseek-key
```

Add those lines to `~/.zshrc` or `~/.bashrc` to make them permanent.

## Usage

```bash
love <word>          # look a word up
love --review        # work through today's review
love --daily         # build and send today's email
love --backfill      # fill in phonics and word parts for older words
love --backfill --recheck   # re-check existing splits against the morphology data
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

## The daily email

This is the point of the whole thing: an email that arrives on its own so the
words can be read from a phone, away from the computer. Rating still happens at
the computer — the email carries the material, `love --review` records what you
remembered.

`--daily` plans the day, generates practice material for any word that lacks it,
renders the digest, and sends it.

### Sending

The mail settings are read from the environment:

| Variable | Default | Meaning |
|---|---|---|
| `SMTP_HOST` | `smtp.qq.com` | mail server |
| `SMTP_PORT` | `465` | implicit TLS |
| `SMTP_USER` | — | the account; used as sender and default recipient |
| `SMTP_PASSWORD_FILE` | — | **preferred**: path to a file holding the password |
| `QQ_SMTP_AUTH_CODE` | — | the authorization code, if no file is given |
| `LOVE_MAIL_TO` | `SMTP_USER` | recipient |

QQ Mail does not accept the account password over SMTP. It requires a 16-digit
authorization code, generated in the web UI under 设置 → 账户 →
"POP3/IMAP/SMTP服务" → 生成授权码.

```bash
printf '%s' 'your-16-digit-code' > ~/.ewh/smtp-password && chmod 600 ~/.ewh/smtp-password
export SMTP_USER=you@qq.com
export SMTP_PASSWORD_FILE=~/.ewh/smtp-password
love --daily --dry-run     # check the content first
love --daily               # then send one
```

### Scheduling it

```bash
love --install-schedule --at 07:30,12:30,20:30
love --install-schedule --now      # install and fire once, to prove it works
love --uninstall-schedule
```

This writes a launch agent to `~/Library/LaunchAgents/dev.love.daily.plist` and
loads it. There is no daemon: a job that runs once a day does not justify a
resident process, and a daemon that happens not to be running at the scheduled
minute does nothing at all, silently. launchd also runs the job after the laptop
wakes, so a missed morning is not a missed day.

**The installer refuses to put a secret in the property list.** A plist is
readable by anything running as the user and `launchctl print` reproduces it in
full, so only paths are recorded — `SMTP_PASSWORD_FILE` and
`DEEPSEEK_API_KEY_FILE`. Given an inline secret it fails, explains how to create
the file, and does not echo the secret back.

Output goes to `~/.ewh/logs/daily.log` and `daily.err`. If a morning ever passes
without an email, those two files are the first place to look.

> Multiple times send the **same digest**: it is built from the day's plan, and
> the plan only changes when a review is graded. Three sends are three
> opportunities to read the same material, not three different lessons.

## Output

Exactly four lines, always:

```text
word /ipa/
Phonics: <the word split into sound chunks, with each chunk's pronunciation>
Parts: <prefix · root · suffix, each with its meaning ⇒ "the literal sense">
ELI5: <a very simple English explanation>
```

A word that predates the form layer prints two of those lines rather than two
empty labels — see [Upgrading to the form layer](#upgrading-to-the-form-layer).

Nothing else is printed on success: no JSON, no file paths, no "saved"
messages, and no Chinese gloss. The terminal is where a word is recalled, and
the gloss is the answer to that test; the daily email still carries it, because
reading on a phone is not the same act as remembering at a desk. An action may
report progress — `love --backfill` says how many words it is filling — but a
lookup prints the block above and nothing more.

The notation is fixed. Chunks and word parts are joined with the middle dot
`·`, `→` maps a spelling to its sounds, and `⇒` maps a construction to its
literal sense. The middle dot is not decoration: `-` belongs to the word itself
in a word like `ex-husband`, so it cannot double as a separator.

On a terminal the head line is bold; when piped or when `NO_COLOR` is set,
output is plain text.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `DEEPSEEK_API_KEY` | — | Your DeepSeek API key. Required for a new word. |
| `DEEPSEEK_API_KEY_FILE` | — | Path to a file holding the key; preferred, and takes precedence. |
| `DEEPSEEK_BASE_URL` | `https://api.deepseek.com/v1` | API base URL. |
| `EWH_DIR` | `~/.ewh` | Directory holding the store. |
| `EWH_CACHE` | — | Words file path, kept for backwards compatibility. |
| `EWH_MODEL` | `deepseek-v4-flash` | Model id. |
| `NO_COLOR` | — | Disable colored output when set. |

The mail settings are listed under [The daily email](#the-daily-email).

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

One JSON object per line, eleven fields, nothing else:

```json
{"id":"4adb2c8c","word":"evil","normalized":"evil","ipa":"/ˈiːvəl/","phonics":"e·vil → /ˈiː/ · /vəl/","parts":"evil (bad, harmful) ⇒ \"bad, harmful\"","parts_source":"morphology","eli5":"Very, very bad.","chinese":"邪恶的","source":"cli","created_at":"2026-09-13T14:47:22+08:00"}
```

`phonics`, `parts` and `parts_source` are omitted when a record predates them, so
a line written by an older version keeps its original bytes until something
upgrades it.

`parts_source` says where a segmentation came from, and it is the field that
makes the rest honest:

| Value | Meaning |
|---|---|
| `model` | the morphology data had nothing to say, so the model worked it out |
| `morphology` | the data supplied the split and the model agreed with it |
| `override` | a hand correction in `internal/lexicon/overrides.jsonl` |
| `morphology-compound` | the model read it as a compound of two ordinary words |
| `morphology-declined` | the model refused every recorded analysis |
| `morphology-forced` | the model disagreed and the data won |

The store is four files under the store directory, plus an optional data
directory:

| File | Role | Rebuildable |
|---|---|---|
| `words.jsonl` | your words | ❌ an asset |
| `reviews.jsonl` | your learning history, append-only | ❌ the only history |
| `memory.json` | derived scheduling state | ✅ from the two above |
| `generated.jsonl` | cached example sentences and dialogue | ✅ costs one API call |
| `lexicon/` | how words are really built (optional) | ✅ re-run the build script |

The split matters. `words.jsonl` holds the anchor layer — IPA, the phonics
split, the word parts, the child-simple explanation, one Chinese meaning — and
it is never regenerated, because memory depends on a stable cue. `generated.jsonl`
holds the expansion layer, which is only practice material and is free to be
rebuilt.

`id` and `normalized` exist so a word has a stable identity and a single
comparison key. `normalized` is stored rather than recomputed, so a future
change to the normalization rule cannot silently re-partition existing data.

- A cache **hit** never writes to the file, so queries do not churn it.
- A cache **miss** appends exactly one line, under a lock, and re-checks for a
  duplicate first. Two simultaneous lookups cannot produce two records, and
  adding the same word twice is idempotent.
- `--backfill` (and a `--daily` run) updates an existing record **in place**,
  under the same lock, and writes only `phonics` and `parts`. The line keeps its
  position and its anchor.
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

### Upgrading to the form layer

That upgrade is offline, and an offline rewrite cannot invent a phonics split or
an etymology. A record that lacks only `phonics` and `parts` therefore keeps
working, and prints without those two lines:

```console
$ love evil
evil /ˈiːvəl/
ELI5: Very, very bad.
```

Two paths fill the gap, and both are paths that already spend requests:

| Path | Scope | Cost |
|---|---|---|
| `love --backfill` | every record that needs it | one request per word |
| `love --daily` | the words today's digest will show | one request per word, once ever |

A lookup never does it. A cache hit is promised to be offline and free, and
that promise is worth more than a faster upgrade — the record is served as it
stands until one of the two paths above touches it.

## The morphology data layer

`parts` is the one field a model should not be trusted to invent. Asked to split
a word, it produces something plausible every time: it once split `symlink` as
`sym- (together)`, which is not where that `sym` comes from, and `fundamentals`
as `-amental`, which is not a morpheme. So the segmentation is looked up instead.

The data lives beside your word file, in `~/.ewh/lexicon/`, and is optional.
Without it everything still works exactly as before, and `parts_source` says
`model` for every word.

### Building it

```bash
python3 scripts/build_morphology.py         # ~30 MB download, then ~14 MB on disk
python3 scripts/build_morphology.py --force # re-download the sources
```

It reads [MorphyNet](https://github.com/kbatsuren/MorphyNet), which is
Wiktionary's morpheme segmentations already parsed into a table, and writes
three indexes plus a `sources.json` recording the exact inputs and their hashes:

| File | What it holds |
|---|---|
| `derivations.tsv` | target, source, morpheme, type — 213k rows |
| `inflections.tsv` | form, lemma, ending — 188k rows |
| `lemmas.txt` | 316k lemmas, for "is this a word" checks |

> **Licence.** The indexes are derived from Wiktionary and are **CC BY-SA 3.0**,
> separately from the MIT-licensed code that reads them. That is why they are
> built next to your word file rather than committed to this repository, and why
> the build writes the attribution next to them.

### What it changes

For a word the data knows, the model is shown the recorded analyses and must use
one of them; if it drifts, the analysis is rewritten from the data and the
record says `morphology-forced`. The model keeps the jobs it is good at: glossing
a free root and writing the literal sense. It can also refuse — `no clear prefix
or suffix` is always an acceptable answer, and the only one that can save a
coinage like `symlink` from a tidy, wrong story.

The curated half — 426 prefixes, suffixes and roots with their meanings — is
compiled into the binary (`internal/lexicon/affixes.jsonl`), which is also what
lets the tool explain Greek compounds like `biology` and `telegraph` that no
derivation table records.

### When the data is wrong

It sometimes is. Wiktionary derives `curious` from the element `curium`, and no
table knows that `sym` in `symlink` is a clipping of `symbolic`. Those words go
in `internal/lexicon/overrides.jsonl`, which outranks everything and survives a
re-check. Currently one word is corrected there.

`love --backfill --recheck` re-examines words whose stored split no longer
matches the data — after a rebuild, a table edit, or a new correction. A word the
data already agrees with costs no request, so running it is free when nothing
has changed.

### The tail

Words nothing can speak for — coinages, proper nouns, typos — are explained by
the model alone and marked `parts_source: model`. That answer is provisional and
nothing can verify it, so `love --backfill` ends by naming those words rather
than leaving a plausible guess indistinguishable from a recorded fact:

```console
$ love --backfill
每个词都已经有拼读和构词，没有需要补齐的。

以下 3 个词的构词只有模型给过意见，词法数据无法验证：
  caommunication
  literrally
  venice
```

Anything you check belongs in `internal/lexicon/overrides.jsonl`. An override
outranks the data, and a re-check will not undo it.

### Measuring it

`internal/lexicon/testdata/parts_gold.jsonl` is a hand-checked answer key of 56
common words. It runs as a test, and skips when no data layer is installed:

```bash
go test ./internal/lexicon -run TestGoldSet -v
```

The current score is **55/55 of the words the data has an opinion on** (one of
them via a hand correction). Before the table-driven splitter existed it was
44/55, and the misses were exactly the Greek compounds and the over-split
familiar words.

## Testing

Four layers, cheapest first. The first three need **no API key** and cost
nothing, so they are the ones to run on every change.

| Layer | Command | Key | Cost | Covers |
|---|---|---|---|---|
| 1. Unit | `go test ./...` | no | free | normalization, tolerant reading, first-record-wins, dedup append, in-place form-layer upgrade, segmentation from data, the gold set, HTTP parsing, retry and fallback, render format |
| 2. Race | `go test -race -count=1 ./...` | no | free | concurrent appends under the write lock |
| 3. End-to-end, mocked | `python3 scripts/e2e_mock_check.py` | no | free | the real binary over real HTTP: generate → persist → hit → backfill, plus every exit code |
| 4. Real API | `love <new-word>` then `love <same-word>` | yes | ~1 call | the real model, real billing, real persistence |

`scripts/e2e_mock_check.py` spawns a throwaway local HTTP server and drives the
compiled binary against it. It asserts 85 conditions, including that the second
lookup makes **zero** requests, that a backfill rewrites only the two form
fields and leaves the anchor alone, and that failures never write to the word
file. Set `LOVE_BIN=/path/to/love` to test a binary that is not on `PATH`.

### Manual smoke checklist

```bash
love evil                                   # cache hit, no key needed  -> 0
love SIGN                                   # case-insensitive           -> 0
love "  symlink "                           # trimming and phrases       -> 0
love --nope                                 # unknown flag               -> 2
love                                        # no word                    -> 2
env -u DEEPSEEK_API_KEY love serendipity    # miss without a key         -> 2, empty stdout
love --backfill                             # nothing missing            -> 0, no key needed
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
| M6 | the form layer: phonics and word parts, and `--backfill` to add them to older words | ✅ |
| M7 | the morphology data layer: real segmentations, a curated affix table, and a gold set to measure them | ✅ |
| | `--stats`, `--export` | planned |
| | a Reminders adapter, and a launchd job to run `--daily` each morning | planned |

`docs/v0.3-review.md` holds the current design review and the milestone detail;
`docs/cli-design.md` explains the command grammar; `docs/content-layers.md`
explains why the content is split into layers.

Two earlier documents are kept as history rather than as guidance, because they
record what was proposed and what was wrong with it:
`docs/memory-engine-assessment.md` (the v0.1 specification) and
`docs/v0.2-review.md` (the v0.2 specification). The syntax they quote —
`love review`, `love daily` — is not today's; that is the point of
`docs/cli-design.md`.

## Note on the name

`love` collides with the LÖVE (love2d) game engine's executable. If you have
that installed, use an explicit path or a shell alias.

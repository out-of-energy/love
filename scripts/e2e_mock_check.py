#!/usr/bin/env python3
"""End-to-end check of the installed `love` binary against a local mock API.

This exercises the whole binary -- argument parsing, HTTP, response parsing,
JSONL persistence, rendering and exit codes -- without touching the real
DeepSeek API or spending any credits.

Usage: python3 scripts/e2e_mock_check.py
"""

import http.server
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import threading

BINARY = os.environ.get("LOVE_BIN", "love")


class MockAPI(http.server.BaseHTTPRequestHandler):
    """Serves the DeepSeek chat-completions envelope for one fixed word."""

    calls = 0
    reject_thinking = False
    record = {
        "word": "serendipity",
        "ipa": "/ˌserənˈdɪpəti/",
        "phonics": "ser·en·dip·i·ty → /ˌser/ · /ən/ · /ˈdɪp/ · /ə/ · /ti/",
        "parts": 'Serendip (old name of Sri Lanka) · -ity (state of) ⇒ "the state of Serendip"',
        "eli5": "A happy thing you find by chance.",
        "chinese": "意外发现美好事物",
    }

    def do_POST(self):
        type(self).calls += 1
        length = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(length) or b"{}")

        if self.headers.get("Authorization") != "Bearer test-key":
            self._send(401, {"error": {"message": "Authentication Fails", "code": "invalid_api_key"}})
            return
        if type(self).reject_thinking and "thinking" in body:
            self._send(400, {"error": {"message": "unknown field thinking", "code": "invalid_request_error"}})
            return

        # One endpoint serves two contracts. The anchor lookup and the expansion
        # generator are told apart by their system prompt, which is how the real
        # client distinguishes them too. The dialogue must contain the target
        # word or the client is right to reject it.
        system = body.get("messages", [{}])[0].get("content", "")
        word = body.get("messages", [{}, {}])[1].get("content", "word")

        if "dialogue" in system:
            content = json.dumps({
                "meaning": "to keep something in good condition",
                "examples": [f"I {word} it every month."],
                "scene": "Someone taking care of what they own.",
                "dialogue": [
                    {"speaker": "A", "line": "Your bike still looks new."},
                    {"speaker": "B", "line": f"I {word} it every month."},
                ],
            }, ensure_ascii=False)
        else:
            content = json.dumps(type(self).record, ensure_ascii=False)

        self._send(200, {"choices": [{"message": {"role": "assistant", "content": content}}]})

    def _send(self, status, payload):
        data = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *args):
        pass


def start_server():
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), MockAPI)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, f"http://127.0.0.1:{srv.server_address[1]}/v1"


def resolve_binary():
    """Return a runnable path for BINARY, whether it is a name on PATH or an
    explicit relative/absolute path such as ./love."""
    if os.sep in BINARY or BINARY.startswith("."):
        return BINARY if os.path.isfile(BINARY) else None
    for directory in os.environ.get("PATH", "").split(os.pathsep):
        if directory and os.path.isfile(os.path.join(directory, BINARY)):
            return os.path.join(directory, BINARY)
    return None


def run_love(binary, args, env):
    # stdin is closed off rather than inherited: a test must never be able to
    # hang waiting for input, and an interactive path must fail loudly instead.
    return subprocess.run(
        [binary, *args], capture_output=True, text=True, env=env, stdin=subprocess.DEVNULL
    )


def check(label, condition, detail=""):
    print(f"  {'PASS' if condition else 'FAIL'}  {label}")
    if not condition and detail:
        print(f"        {detail}")
    return condition


def main():
    binary = resolve_binary()
    if binary is None:
        print(f"cannot find {BINARY}; put it on PATH or set LOVE_BIN", file=sys.stderr)
        return 1
    print(f"testing binary: {os.path.abspath(binary)}\n")

    srv, base_url = start_server()
    tmp = pathlib.Path(tempfile.mkdtemp())
    cache = tmp / "words.jsonl"
    env = {
        **os.environ,
        "DEEPSEEK_BASE_URL": base_url,
        "DEEPSEEK_API_KEY": "test-key",
        "EWH_CACHE": str(cache),
    }

    ok = True
    print("scenario 1: cache miss generates, persists, and renders")
    MockAPI.calls = 0
    first = run_love(binary, ["Serendipity"], env)
    expected = (
        "serendipity /ˌserənˈdɪpəti/\n"
        "Phonics: ser·en·dip·i·ty → /ˌser/ · /ən/ · /ˈdɪp/ · /ə/ · /ti/\n"
        'Parts: Serendip (old name of Sri Lanka) · -ity (state of) ⇒ "the state of Serendip"\n'
        "ELI5: A happy thing you find by chance.\n"
    )
    ok &= check("exit code 0", first.returncode == 0, first.stderr)
    ok &= check("output matches the agreed shape", first.stdout == expected, repr(first.stdout))
    # The gloss is stored, never printed: the terminal is where recall is
    # tested and the gloss is the answer. The mail still carries it.
    ok &= check("the terminal block carries no gloss", "中文" not in first.stdout, repr(first.stdout))
    ok &= check("exactly one API call", MockAPI.calls == 1, f"{MockAPI.calls} calls")
    ok &= check("word file created", cache.is_file())

    lines = [l for l in cache.read_text(encoding="utf-8").splitlines() if l.strip()]
    ok &= check("one line stored", len(lines) == 1, repr(lines))
    if lines:
        stored = json.loads(lines[0])
        schema = [
            "chinese", "created_at", "eli5", "id", "ipa", "normalized",
            "parts", "parts_source", "phonics", "phonics_source", "source", "word",
        ]
        ok &= check("exactly the documented fields", sorted(stored) == schema, str(sorted(stored)))
        ok &= check("stored word is normalized", stored["word"] == "serendipity", stored.get("word", ""))
        ok &= check("normalized key is present", stored.get("normalized") == "serendipity", stored.get("normalized", ""))
        ok &= check("an id was assigned", bool(stored.get("id")), repr(stored.get("id")))
        ok &= check("source defaults to cli", stored.get("source") == "cli", repr(stored.get("source")))
        ok &= check("created_at was recorded", bool(stored.get("created_at")), repr(stored.get("created_at")))
        ok &= check(
            "the form layer was stored",
            stored.get("phonics", "").startswith("ser·en") and "⇒" in stored.get("parts", ""),
            str(stored),
        )
    first_id = json.loads(lines[0])["id"] if lines else None

    print("scenario 2: the second lookup is served from cache, with no network")
    MockAPI.calls = 0
    second = run_love(binary, ["SERENDIPITY"], env)
    ok &= check("exit code 0", second.returncode == 0, second.stderr)
    ok &= check("identical output", second.stdout == expected, repr(second.stdout))
    ok &= check("zero API calls", MockAPI.calls == 0, f"{MockAPI.calls} calls")
    ok &= check("still one line", len(cache.read_text(encoding='utf-8').strip().splitlines()) == 1)

    print("scenario 3: a rejected thinking control falls back transparently")
    MockAPI.calls = 0
    MockAPI.reject_thinking = True
    fallback_cache = tmp / "fallback.jsonl"
    fallback = run_love(binary, ["serendipity"], {**env, "EWH_CACHE": str(fallback_cache)})
    MockAPI.reject_thinking = False
    ok &= check("exit code 0", fallback.returncode == 0, fallback.stderr)
    ok &= check("output still correct", fallback.stdout == expected, repr(fallback.stdout))
    ok &= check("took two attempts", MockAPI.calls == 2, f"{MockAPI.calls} calls")

    print("scenario 4: a bad key fails fast with exit code 3")
    MockAPI.calls = 0
    bad = run_love(binary, ["serendipity"], {**env, "DEEPSEEK_API_KEY": "wrong", "EWH_CACHE": str(tmp / "bad.jsonl")})
    ok &= check("exit code 3", bad.returncode == 3, f"exit={bad.returncode}")
    ok &= check("not retried", MockAPI.calls == 1, f"{MockAPI.calls} calls")
    ok &= check("nothing written", not (tmp / "bad.jsonl").exists())

    print("scenario 5: a dead endpoint fails with exit code 3 and caches nothing")
    dead_cache = tmp / "dead.jsonl"
    dead = run_love(binary, ["serendipity"], {**env, "DEEPSEEK_BASE_URL": "http://127.0.0.1:9/v1", "EWH_CACHE": str(dead_cache)})
    ok &= check("exit code 3", dead.returncode == 3, f"exit={dead.returncode}")
    ok &= check("nothing written", not dead_cache.exists())

    print("scenario 6: a legacy four-field file is upgraded in place")
    legacy = tmp / "legacy.jsonl"
    legacy_body = (
        '{"word":"evil","ipa":"/\\u02c8i\\u02d0v\\u0259l/","eli5":"Very, very bad.","chinese":"\\u90aa\\u6076\\u7684"}\n'
        '{"word":"sign","ipa":"/sa\\u026an/","eli5":"A sign.","chinese":"\\u6807\\u5fd7"}\n'
    )
    legacy.write_text(legacy_body, encoding="utf-8")
    MockAPI.calls = 0

    migrated = run_love(binary, ["EVIL"], {**env, "EWH_CACHE": str(legacy)})
    ok &= check("exit code 0", migrated.returncode == 0, migrated.stderr)
    ok &= check("served from the legacy record", "Very, very bad." in migrated.stdout, repr(migrated.stdout))
    ok &= check("no network was needed", MockAPI.calls == 0, f"{MockAPI.calls} calls")
    ok &= check("upgrade was announced", "upgraded" in migrated.stderr, repr(migrated.stderr))
    # The offline migration cannot invent a phonics split, so a record that
    # predates the form layer prints without those lines and keeps working.
    ok &= check("no form lines yet", "Phonics:" not in migrated.stdout, repr(migrated.stdout))
    ok &= check("and still no gloss on the terminal", "中文" not in migrated.stdout, repr(migrated.stdout))

    upgraded = [json.loads(l) for l in legacy.read_text(encoding="utf-8").splitlines() if l.strip()]
    ok &= check("both words survived", len(upgraded) == 2, str(len(upgraded)))
    ok &= check(
        "every original field was preserved",
        upgraded[0]["word"] == "evil"
        and upgraded[0]["eli5"] == "Very, very bad."
        and upgraded[0]["chinese"] == "\u90aa\u6076\u7684"
        and upgraded[1]["chinese"] == "\u6807\u5fd7",
        str(upgraded),
    )
    ok &= check(
        "new fields were added",
        all(all(row.get(f) for f in ("id", "normalized", "source", "created_at")) for row in upgraded),
        str(upgraded),
    )
    ok &= check("ids are unique", len({row["id"] for row in upgraded}) == 2, str([r["id"] for r in upgraded]))
    ok &= check("a backup was kept", len(list(tmp.glob("legacy.jsonl.bak-*"))) == 1)

    print("scenario 7: a damaged file is never silently rewritten")
    damaged = tmp / "damaged.jsonl"
    damaged_body = (
        '{"word":"evil","ipa":"/x/","eli5":"Very, very bad.","chinese":"\\u90aa\\u6076\\u7684"}\n'
        "this line is not json at all\n"
    )
    damaged.write_text(damaged_body, encoding="utf-8")
    MockAPI.calls = 0

    survived = run_love(binary, ["evil"], {**env, "EWH_CACHE": str(damaged)})
    ok &= check("exit code 0", survived.returncode == 0, survived.stderr)
    ok &= check("the damaged line was reported", "skipping invalid JSON" in survived.stderr, repr(survived.stderr))
    ok &= check("the healthy record still works", "Very, very bad." in survived.stdout, repr(survived.stdout))
    ok &= check("the file was left untouched", damaged.read_text(encoding="utf-8") == damaged_body)
    ok &= check("no backup was made", len(list(tmp.glob("damaged.jsonl.bak-*"))) == 0)

    print("scenario 8: a word that names an action is still looked up")
    grammar_cache = tmp / "grammar.jsonl"
    grammar_cache.write_text(
        '{"id":"g1","word":"review","normalized":"review","ipa":"/rɪˈvjuː/",'
        '"eli5":"To look at something again.","chinese":"\\u590d\\u4e60",'
        '"source":"cli","created_at":"2026-09-13T00:00:00Z"}\n',
        encoding="utf-8",
    )
    MockAPI.calls = 0
    as_word = run_love(binary, ["review"], {**env, "EWH_CACHE": str(grammar_cache)})
    ok &= check("exit code 0", as_word.returncode == 0, as_word.stderr)
    ok &= check(
        "a word named like an action is looked up as a word",
        "To look at something again." in as_word.stdout,
        repr(as_word.stdout),
    )
    ok &= check("no network was needed", MockAPI.calls == 0, f"{MockAPI.calls} calls")

    print("scenario 9: the action flag never falls through to a lookup")
    as_action = run_love(binary, ["--review"], {**env, "EWH_CACHE": str(grammar_cache)})
    ok &= check("exit code 2", as_action.returncode == 2, f"exit={as_action.returncode}")
    ok &= check(
        "it explains that a terminal is required",
        "\u7ec8\u7aef" in as_action.stderr,
        repr(as_action.stderr),
    )
    ok &= check(
        "it did not quietly print a dictionary entry",
        "To look at something again." not in as_action.stdout,
        repr(as_action.stdout),
    )

    print("scenario 10: an action flag rejects a word argument")
    mixed = run_love(binary, ["--review", "maintain"], {**env, "EWH_CACHE": str(grammar_cache)})
    ok &= check("exit code 2", mixed.returncode == 2, f"exit={mixed.returncode}")
    ok &= check(
        "the two grammars are reported as mutually exclusive",
        "\u4e0d\u63a5\u53d7\u5355\u8bcd\u53c2\u6570" in mixed.stderr,
        repr(mixed.stderr),
    )

    print("scenario 11: --daily generates the expansion layer and renders the mail")
    daily_cache = tmp / "daily.jsonl"
    daily_cache.write_text(
        '{"id":"d1","word":"serendipity","normalized":"serendipity","ipa":"/\\u02ccser\\u0259n\\u02c8d\\u026ap\\u0259ti/",'
        '"eli5":"A happy thing you find by chance.","chinese":"\\u610f\\u5916\\u53d1\\u73b0\\u7f8e\\u597d\\u4e8b\\u7269",'
        '"source":"cli","created_at":"2026-09-01T00:00:00Z"}\n',
        encoding="utf-8",
    )
    out_html = tmp / "today.html"
    MockAPI.calls = 0

    daily = run_love(binary, ["--daily", "--dry-run", "--out", str(out_html)], {**env, "EWH_CACHE": str(daily_cache)})
    ok &= check("exit code 0", daily.returncode == 0, daily.stderr)
    # Two calls: expansion content for the digest, plus the form layer this
    # record predates. Neither happens in a lookup.
    ok &= check("an expansion call and a form-layer call", MockAPI.calls == 2, f"{MockAPI.calls} calls")
    ok &= check("the html file was written", out_html.is_file())

    html = out_html.read_text(encoding="utf-8") if out_html.is_file() else ""
    ok &= check("the anchor layer is in the mail", "A happy thing you find by chance." in html, html[:200])
    ok &= check("the form layer is in the mail", "Phonics" in html and "ser·en" in html, html[:200])
    ok &= check("the gloss is in the mail", "意外发现美好事物" in html, html[:200])
    ok &= check(
        "the expansion layer is in the mail",
        "to keep something in good condition" in html,
        "no expansion found",
    )
    ok &= check(
        "the dialogue, which uses the word, is in the mail",
        "serendipity it every month" in html,
        "no dialogue found",
    )

    filled_cache = [json.loads(l) for l in daily_cache.read_text(encoding="utf-8").splitlines() if l.strip()]
    ok &= check(
        "the form layer was written back to the word file",
        bool(filled_cache) and filled_cache[0].get("phonics", "").startswith("ser·en"),
        str(filled_cache),
    )
    ok &= check(
        "and the anchor it was written beside is untouched",
        bool(filled_cache)
        and filled_cache[0]["id"] == "d1"
        and filled_cache[0]["eli5"] == "A happy thing you find by chance.",
        str(filled_cache),
    )

    generated = tmp / "generated.jsonl"
    ok &= check("the expansion was cached", generated.is_file())
    if generated.is_file():
        rows = [json.loads(l) for l in generated.read_text(encoding="utf-8").splitlines() if l.strip()]
        ok &= check("one cached record", len(rows) == 1, str(len(rows)))
        if rows:
            ok &= check("it records which provider produced it", rows[0].get("provider") == "deepseek", str(rows[0].get("provider")))

    print("scenario 12: a cached expansion is never regenerated")
    MockAPI.calls = 0
    again = run_love(binary, ["--daily", "--dry-run", "--out", str(tmp / "again.html")], {**env, "EWH_CACHE": str(daily_cache)})
    ok &= check("exit code 0", again.returncode == 0, again.stderr)
    ok &= check("zero generation calls", MockAPI.calls == 0, f"{MockAPI.calls} calls")

    print("scenario 13: --backfill upgrades only what the migration cannot")
    backfill_cache = tmp / "backfill.jsonl"
    backfill_body = (
        '{"id":"b1","word":"evil","normalized":"evil","ipa":"/x/","eli5":"Very, very bad.",'
        '"chinese":"\\u90aa\\u6076\\u7684","source":"cli","created_at":"2026-09-01T00:00:00Z"}\n'
        '{"id":"b2","word":"sign","normalized":"sign","ipa":"/y/","eli5":"A sign.",'
        '"chinese":"\\u6807\\u5fd7","source":"cli","created_at":"2026-09-01T00:00:00Z"}\n'
    )
    backfill_cache.write_text(backfill_body, encoding="utf-8")
    before = [json.loads(l) for l in backfill_body.splitlines() if l.strip()]
    MockAPI.calls = 0

    filled = run_love(binary, ["--backfill"], {**env, "EWH_CACHE": str(backfill_cache)})
    ok &= check("exit code 0", filled.returncode == 0, filled.stderr)
    ok &= check("one request per word", MockAPI.calls == 2, f"{MockAPI.calls} calls")

    after = [json.loads(l) for l in backfill_cache.read_text(encoding="utf-8").splitlines() if l.strip()]
    ok &= check("still one record per word", len(after) == 2, str(len(after)))
    ok &= check(
        "every record gained the form layer",
        all(r.get("phonics") and r.get("parts") for r in after),
        str(after),
    )
    ok &= check(
        "the file order was kept",
        [r["word"] for r in after] == ["evil", "sign"],
        str([r["word"] for r in after]),
    )
    ok &= check(
        "nothing else about the anchor changed",
        all(
            a["id"] == b["id"] and a["word"] == b["word"] and a["normalized"] == b["normalized"]
            and a["ipa"] == b["ipa"] and a["eli5"] == b["eli5"] and a["chinese"] == b["chinese"]
            and a["source"] == b["source"] and a["created_at"] == b["created_at"]
            for a, b in zip(after, before)
        ),
        f"{before} -> {after}",
    )

    print("scenario 14: a lookup after the backfill is a hit that prints the form layer")
    MockAPI.calls = 0
    hit = run_love(binary, ["evil"], {**env, "EWH_CACHE": str(backfill_cache)})
    ok &= check("exit code 0", hit.returncode == 0, hit.stderr)
    ok &= check("zero API calls", MockAPI.calls == 0, f"{MockAPI.calls} calls")
    ok &= check("the form layer is printed", "Phonics: ser·en" in hit.stdout, repr(hit.stdout))

    print("scenario 15: --backfill is a no-op on a complete file, and needs no key")
    MockAPI.calls = 0
    complete = run_love(
        binary, ["--backfill"], {**env, "EWH_CACHE": str(backfill_cache), "DEEPSEEK_API_KEY": ""}
    )
    ok &= check("exit code 0", complete.returncode == 0, complete.stderr)
    ok &= check("zero API calls", MockAPI.calls == 0, f"{MockAPI.calls} calls")
    ok &= check("it says there is nothing to do", "没有需要补齐的" in complete.stdout, repr(complete.stdout))

    print("scenario 16: --backfill without a key explains itself instead of guessing")
    needs_key = run_love(
        binary, ["--backfill"], {**env, "EWH_CACHE": str(legacy), "DEEPSEEK_API_KEY": ""}
    )
    ok &= check("exit code 2", needs_key.returncode == 2, f"exit={needs_key.returncode}")
    ok &= check("it names the missing key", "DEEPSEEK_API_KEY" in needs_key.stderr, repr(needs_key.stderr))
    ok &= check("stdout stays empty", needs_key.stdout == "", repr(needs_key.stdout))

    print("scenario 17: the morphology data decides the segmentation, not the model")
    # A miniature data layer beside the word file: "serendipity" is recorded as
    # one underived word, and the mock model insists on splitting it. The data
    # must win, and the word must say so in parts_source.
    morph_cache = tmp / "morph.jsonl"
    morph_cache.write_text("", encoding="utf-8")
    morph_dir = tmp / "lexicon"
    morph_dir.mkdir(exist_ok=True)
    # The segmentation files must hold real rows: an empty file is not a data
    # layer, because a lemma list alone cannot tell a derived word from an
    # underived one, and the tool would start refusing splits it should allow.
    (morph_dir / "derivations.tsv").write_text("unhappy\thappy\tun\tprefix\n", encoding="utf-8")
    (morph_dir / "inflections.tsv").write_text("infusing\tinfuse\ting\n", encoding="utf-8")
    (morph_dir / "lemmas.txt").write_text(
        "happy\nserendipity\nsymlink\nunhappy\n", encoding="utf-8"
    )
    MockAPI.record["parts"] = 'ser·en (around) · dip (sink) ⇒ "a lucky find"'
    MockAPI.calls = 0
    try:
        governed = run_love(binary, ["serendipity"], {**env, "EWH_CACHE": str(morph_cache)})
    finally:
        MockAPI.record["parts"] = 'Serendip (old name of Sri Lanka) · -ity (state of) ⇒ "the state of Serendip"'

    ok &= check("exit code 0", governed.returncode == 0, governed.stderr)
    rows = [json.loads(l) for l in morph_cache.read_text(encoding="utf-8").splitlines() if l.strip()]
    ok &= check("one record", len(rows) == 1, str(rows))
    if rows:
        stored = rows[0]
        ok &= check(
            "the invented split was replaced by the recorded one",
            "ser·en" not in stored.get("parts", "") and stored.get("parts", "").startswith("serendipity"),
            stored.get("parts", ""),
        )
        ok &= check(
            "and the record says where the split came from",
            stored.get("parts_source") == "morphology-forced",
            stored.get("parts_source", ""),
        )
        ok &= check(
            "the model's other fields were kept",
            stored.get("ipa") == "/ˌserənˈdɪpəti/" and stored.get("chinese") == "意外发现美好事物",
            str(stored),
        )

    srv.shutdown()
    print()
    print("ALL CHECKS PASSED" if ok else "SOME CHECKS FAILED")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())

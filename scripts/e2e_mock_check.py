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
    return subprocess.run([binary, *args], capture_output=True, text=True, env=env)


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
        "serendipity /ˌserənˈdɪpəti/\n\n"
        "ELI5: A happy thing you find by chance.\n\n"
        "中文：意外发现美好事物\n"
    )
    ok &= check("exit code 0", first.returncode == 0, first.stderr)
    ok &= check("output matches the agreed shape", first.stdout == expected, repr(first.stdout))
    ok &= check("exactly one API call", MockAPI.calls == 1, f"{MockAPI.calls} calls")
    ok &= check("word file created", cache.is_file())

    lines = [l for l in cache.read_text(encoding="utf-8").splitlines() if l.strip()]
    ok &= check("one line stored", len(lines) == 1, repr(lines))
    if lines:
        stored = json.loads(lines[0])
        ok &= check("exactly four fields", sorted(stored) == ["chinese", "eli5", "ipa", "word"], str(sorted(stored)))
        ok &= check("stored word is normalized", stored["word"] == "serendipity", stored.get("word", ""))

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

    srv.shutdown()
    print()
    print("ALL CHECKS PASSED" if ok else "SOME CHECKS FAILED")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())

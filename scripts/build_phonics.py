#!/usr/bin/env python3
"""Build the pronunciation data layer that `love` reads at run time.

`parts` was not the only line a model should not have been trusted with. Asked
for the syllables of `profiling`, it answered:

    pro·fil·ing → /ˈprəʊ/ · /faɪl/ · /ɪŋ/

which splits by letters. `profile` is /ˈprəʊ.faɪl/: the /l/ closes that syllable
only because nothing follows it. Add -ing and English hands the /l/ to the new
syllable as its onset, and every dictionary writes:

    pro·fil·ing → /ˈprəʊ/ · /faɪ/ · /lɪŋ/

The model's version also teaches a rule that is simply false — `fil` is /fɪl/ in
filter, film and filth — and a learner has no way to catch it. So the sounds come
from data.

What it builds, in the target directory (default ~/.ewh/lexicon):

    phonics.tsv   word <TAB> ipa <TAB> sound chunks <TAB> spelling chunks <TAB> source
    sources.json  provenance: the URLs, their sha256, and the licences
    README.md     the same, in prose, next to the data

Inputs, cached beside the output under `_sources/`:

    ipa-dict (MIT)         the transcription, British list first, American as
                           a fallback for words the British list lacks
    Moby Hyphenator (PD)   the orthographic syllables, and a syllable count that
                           settles cases the sounds alone cannot

Two decisions are worth stating.

The syllable boundaries are not in either source: ipa-dict gives a transcription
with stress marks and no divisions, and Moby divides the *spelling*. So the
boundaries are computed — maximal onset over the dictionary's own transcription,
with one refinement English needs (a stressed lax vowel keeps a following
consonant in its coda, which is why "literally" is /ˈlɪt/ · /ə/ and not
/ˈlɪ/ · /tə/) — and Moby's count is used as a constraint where the sound alone is
ambiguous, because "ɪə" is one syllable in "here" and two in "curious".

The stress mark is normalised to the front of its syllable. ipa-dict writes
/sˈaɪn/, with the mark before the vowel; printed beside per-chunk output that
leads with the stress, the two look like different conventions in one record.

Usage:
    python3 scripts/build_phonics.py                 # build into ~/.ewh/lexicon
    python3 scripts/build_phonics.py --out DIR
    python3 scripts/build_phonics.py --force         # re-download the sources
"""

import argparse
import datetime
import hashlib
import json
import os
import pathlib
import shutil
import subprocess
import sys
import urllib.request

IPA_SOURCES = {
    "en_UK": "https://raw.githubusercontent.com/open-dict-data/ipa-dict/master/data/en_UK.txt",
    "en_US": "https://raw.githubusercontent.com/open-dict-data/ipa-dict/master/data/en_US.txt",
}
MOBY_URL = "https://www.gutenberg.org/files/3204/files/mhyph.txt"
IPA_LICENCE = "MIT (open-dict-data/ipa-dict)"
MOBY_LICENCE = "public domain in the US (Project Gutenberg ebook 3204, Grady Ward)"

# The characters that actually occur in ipa-dict's British list — inventoried
# rather than remembered, because the symbol set is where a hand-written
# syllabifier goes quietly wrong (`ɛ` is not `e`; `ɡ` is not `g`; `ɹ` is not `r`).
VOWELS = set("ɪəiɛæeʊaɐɒʌɔuɑɜ")
DIPHTHONGS = {"aɪ", "aʊ", "eɪ", "eə", "ɔɪ", "əʊ", "ɪə", "ʊə"}
CHECKED = set("ɪɛæɒʌʊ")  # vowels English will not let end a syllable
CONSONANTS = set("tsnldkɹzmpbʃfŋɡvwʒhjθðrxɬ")
AFFRICATES = {"tʃ", "dʒ"}
LENGTH = "ː"
NASAL = "̃"
STRESS = ("ˈ", "ˌ")

# Legal onsets. A short hand-written list is the classic bug: "infusing" is
# /ɪnfjˈuːzɪŋ/, and without "fj" the /f/ stays in the coda and the word comes out
# a syllable wrong. So the clusters are generated rather than listed, and the
# rare ones a language actually uses are added by hand.
def _onsets():
    singles = {c for c in CONSONANTS if c != "ŋ"} | {"tʃ", "dʒ"}
    clusters = set()
    for c in "pbtbdkɡfvθðszʃhmnlɹw":
        for glide in "jɹlw":
            if c != glide:
                clusters.add(c + glide)
    clusters |= {
        "sp", "st", "sk", "sm", "sn", "sw", "sf",
        "spl", "spɹ", "stɹ", "skɹ", "skw", "skj", "spj", "stj",
        "θw", "ʃɹ", "hj", "kv", "sv", "tv", "dv",
    }
    return singles | {c for c in clusters if all(ch in CONSONANTS or ch in "jw" for ch in c)}


ONSETS = _onsets()


def fetch(url, timeout=180):
    """Fetch a URL, preferring curl when it is available.

    Python's TLS through a CONNECT proxy fails in some environments where curl
    works, and it fails as an opaque handshake timeout — the build should not
    depend on which TLS stack happens to be usable that day. curl ships with
    macOS and nearly every Linux, so it is tried first and urllib is the
    fallback rather than the other way round.
    """
    if shutil.which("curl"):
        result = subprocess.run(
            ["curl", "-fsSL", "--max-time", str(timeout), url],
            capture_output=True,
        )
        if result.returncode == 0 and result.stdout:
            return result.stdout
    with urllib.request.urlopen(url, timeout=timeout) as response:
        return response.read()


def download(url, dest, force=False):
    if dest.exists() and not force and dest.stat().st_size > 0:
        return dest
    dest.parent.mkdir(parents=True, exist_ok=True)
    print(f"  downloading {url}")
    tmp = dest.with_suffix(dest.suffix + ".part")
    tmp.write_bytes(fetch(url))
    tmp.replace(dest)
    return dest


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


# ---------------------------------------------------------------------------
# The syllabifier.
# ---------------------------------------------------------------------------


def tokenize(ipa):
    """Split a transcription into symbols, longest first.

    Only the affricates are kept whole here. A diphthong is deliberately NOT a
    token: whether "ɪə" is one syllable or two is the question this file has to
    answer, and a tokenizer that has already fused the pair cannot un-fuse it
    when a hyphenation source says the word has one syllable more.
    """
    ipa = ipa.strip().strip("/")
    out, i = [], 0
    multi = sorted(AFFRICATES, key=len, reverse=True)
    while i < len(ipa):
        for sym in multi:
            if ipa.startswith(sym, i):
                out.append(sym)
                i += len(sym)
                break
        else:
            char = ipa[i]
            # Length and nasality belong to the vowel before them: "uː" is one
            # nucleus, not a vowel followed by something.
            if char in (LENGTH, NASAL) and out:
                out[-1] += char
            else:
                out.append(char)
            i += 1
    return out


def vowel_runs(tokens):
    """Maximal stretches of adjacent vowels, as lists of token indices."""
    positions = [i for i, t in enumerate(tokens) if t and t[0] in VOWELS]
    runs, current = [], []
    for i in positions:
        if current and i == current[-1] + 1:
            current.append(i)
        else:
            if current:
                runs.append(current)
            current = [i]
    if current:
        runs.append(current)
    return runs


def nuclei(tokens, expected=0):
    """Return (start, end) spans, one per syllable nucleus.

    The number of syllables is decided by the hyphenation source when there is
    one, and by a prior about diphthongs when there is not. That order matters,
    because the sounds alone cannot settle it: "ɪə" is one syllable in "here" and
    two in "curious", and the transcriptions are otherwise identical in shape.

    Given a count, the run is built by merging every adjacent pair and then
    re-splitting from the right until the count is met. "curious" needs one
    split, and the rightmost pair is the one that should split — which is what
    makes /ˈkjʊɹ/ · /ɪ/ · /əs/ come out instead of /ˈkjʊ/ · /ɹɪəs/.
    """
    runs = vowel_runs(tokens)
    counts = [1] * len(runs)

    if expected and expected > len(runs):
        for _ in range(expected - len(runs)):
            target = next((r for r in range(len(runs) - 1, -1, -1) if counts[r] < len(runs[r])), None)
            if target is None:
                break
            counts[target] += 1
    elif not expected:
        for r, positions in enumerate(runs):
            if len(positions) < 2:
                continue
            # No count to go by: keep the canonical diphthongs together and
            # split anything else.
            if "".join(tokens[p] for p in positions) not in DIPHTHONGS | {"iə", "uə", "eə", "ɔə"}:
                counts[r] = len(positions)

    spans = []
    for r, positions in enumerate(runs):
        want = counts[r]
        if want <= 1:
            spans.append((positions[0], positions[-1] + 1))
            continue
        cut = len(positions) - (want - 1)
        spans.append((positions[0], positions[cut - 1] + 1))
        for position in positions[cut:]:
            spans.append((position, position + 1))
    return spans


def stress_of(tokens, span):
    """The stress mark belonging to a nucleus: the one immediately before it."""
    start = span[0]
    if start > 0 and tokens[start - 1] in STRESS:
        return tokens[start - 1]
    return ""


def nucleus_text(tokens, span):
    return "".join(tokens[span[0]:span[1]])


def is_checked(tokens, span):
    """Whether a nucleus is a checked vowel.

    English has checked vowels (ɪ ɛ æ ɒ ʌ ʊ) and free ones. A checked vowel
    cannot end a syllable, so a consonant must stay behind it: "city" is
    /ˈsɪt/ · /i/, never /ˈsɪ/ · /ti/. This one rule is what the first version of
    this script was missing, and it is why "literally", "family", "chocolate"
    and "education" all came out a consonant early.
    """
    return nucleus_text(tokens, span).rstrip(LENGTH + NASAL) in CHECKED


def split_points(tokens, spans):
    """The token index each syllable starts at, for every syllable after the first."""
    cuts = []
    for index in range(len(spans) - 1):
        span, nxt = spans[index], spans[index + 1]
        positions = [i for i in range(span[1], nxt[0]) if tokens[i] not in STRESS]
        run = [tokens[i] for i in positions]

        # Maximal onset: give the next syllable the longest legal cluster.
        onset = 0
        for n in range(len(run), 0, -1):
            if "".join(run[len(run) - n:]) in ONSETS:
                onset = n
                break

        # ... unless that would leave a checked vowel with no coda at all, in
        # which case one consonant stays behind.
        if run and onset == len(run) and is_checked(tokens, span):
            onset -= 1

        keep = len(run) - onset
        cuts.append(span[1] if keep == 0 else positions[keep - 1] + 1)
    return cuts


def syllabify(ipa, expected=0):
    """Split a transcription into syllables, stress leading each one.

    `expected` is the syllable count a hyphenation source claims. It is used only
    to *un*-merge: a pair like "ɪə" is one syllable in "here" and two in
    "curious", and the sounds alone cannot tell you which. Merging is the default
    because it is right more often, and the count corrects it.
    """
    tokens = tokenize(ipa)
    spans = nuclei(tokens, expected=expected)
    if not spans:
        return ["/" + "".join(t for t in tokens if t not in STRESS) + "/"]

    cuts = split_points(tokens, spans)
    chunks, start = [], 0
    for end in cuts + [len(tokens)]:
        chunks.append(tokens[start:end])
        start = end

    out = []
    for chunk in chunks:
        stress = "".join(t for t in chunk if t in STRESS)
        body = "".join(t for t in chunk if t not in STRESS)
        if not body:
            return None
        out.append("/" + stress + body + "/")
    return out


# ---------------------------------------------------------------------------
# Inputs.
# ---------------------------------------------------------------------------


def read_ipa(path):
    """word -> IPA, first pronunciation only."""
    table = {}
    with open(path, encoding="utf-8") as f:
        for line in f:
            parts = line.rstrip("\n").split("\t")
            if len(parts) < 2:
                continue
            word, ipa = parts[0].strip().lower(), parts[1].split(",")[0].strip()
            if word and ipa.startswith("/"):
                table.setdefault(word, ipa)
    return table


def read_moby(path):
    """word -> its orthographic syllables, as Moby hyphenates them.

    The list is not the file the ebook page offers: that one is the package
    README, and it points here. The breaks are marked with byte 0xA5 rather than
    a hyphen, the text is Latin-1, and the lines are CRLF — three details that
    turn a library of 160,000 hyphenations into an empty table if you guess at
    them instead of looking.

    Moby marks legal *orthographic* breaks, which is not the same thing as the
    sound divisions this tool prints — "com-fort-a-ble" against
    /ˈkʌmf/ · /tə/ · /bəl/, because the "or" is not pronounced. Both are useful:
    the count settles an ambiguous vowel pair, and the spelling is what the line
    prints to the left of the arrow.
    """
    breaks = {}
    sep = b"\xa5"
    body = path.read_bytes()
    start = body.find(b"*** START OF THE PROJECT GUTENBERG EBOOK")
    end = body.find(b"*** END OF THE PROJECT GUTENBERG EBOOK")
    if start != -1:
        body = body[body.find(b"\n", start) + 1:]
    if end != -1:
        body = body[:end]
    skipped = 0
    for line in body.split(b"\n"):
        line = line.strip()
        if not line or b" " in line or b"'" in line:
            skipped += 1
            continue
        parts = [p.decode("latin-1") for p in line.split(sep)]
        if not all(part.isalpha() and part.isascii() for part in parts):
            skipped += 1
            continue
        word = "".join(parts).lower()
        breaks.setdefault(word, [part.lower() for part in parts])
    return breaks, skipped


def build(uk, us, moby):
    words = dict(us)
    words.update(uk)  # British wins where both have the word

    rows, skipped, no_moby, mismatched = [], 0, 0, 0
    for word in sorted(words):
        ipa = words[word]
        syllables = moby.get(word) or []
        expected = len(syllables)
        if not expected:
            no_moby += 1
        chunks = syllabify(ipa, expected=expected)
        if not chunks:
            skipped += 1
            continue
        # A mismatch is reported rather than fatal. Moby hyphenates the spelling
        # and this splits the sound, and the two genuinely differ in words like
        # "comfortable" (com-fort-a-ble against /ˈkʌmf/ · /tə/ · /bəl/); refusing
        # those words would cost coverage for no gain.
        if expected and len(chunks) != expected:
            mismatched += 1

        # The printed transcription is rebuilt from the chunks, so the ipa and
        # the phonics line can never disagree about where a syllable starts.
        normalised = "/" + "".join(c.strip("/") for c in chunks) + "/"
        # Moby's own breaks always win the spelling side, even when the count
        # differs: it is the orthographic truth, and the two sides disagreeing is
        # a fact about English rather than an error to hide.
        spelling = "·".join(syllables)
        source = "uk" if word in uk else "us"
        if spelling:
            source += "+moby"
        rows.append((word, normalised, "/" + "|".join(c.strip("/") for c in chunks), spelling, source))
    return rows, skipped, no_moby, mismatched


def is_sorted(path):
    previous = ""
    with open(path, encoding="utf-8") as f:
        for line in f:
            key = line.split("\t", 1)[0]
            if key < previous:
                return False
            previous = key
    return True


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--out", default=os.path.join(os.path.expanduser("~/.ewh"), "lexicon"))
    ap.add_argument("--force", action="store_true", help="re-download the source files")
    args = ap.parse_args()

    out = pathlib.Path(args.out)
    cache = out / "_sources"
    out.mkdir(parents=True, exist_ok=True)

    print("fetching ipa-dict (MIT) and the Moby hyphenator (public domain)")
    local = {name: download(url, cache / f"{name}.txt", args.force) for name, url in IPA_SOURCES.items()}
    moby_path = download(MOBY_URL, cache / "moby-hyphenator.txt", args.force)

    uk, us = read_ipa(local["en_UK"]), read_ipa(local["en_US"])
    moby, moby_skipped = read_moby(moby_path)
    print(f"  ipa-dict: {len(uk)} British, {len(us)} American")
    print(f"  moby:     {len(moby)} words ({moby_skipped} lines out of scope)")

    print("syllabifying")
    rows, skipped, no_moby, mismatched = build(uk, us, moby)
    path = out / "phonics.tsv"
    with open(path, "w", encoding="utf-8") as f:
        for word, ipa, chunks, spelling, source in rows:
            f.write(f"{word}\t{ipa}\t{chunks}\t{spelling}\t{source}\n")

    print()
    print("verifying the index")
    if not is_sorted(path):
        print("  phonics.tsv is NOT sorted; lookups would silently miss", file=sys.stderr)
        return 1
    print(f"  phonics.tsv        sorted, {len(rows)} rows")

    manifest = {
        "built_at": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds"),
        "script": "scripts/build_phonics.py",
        "licences": {"ipa-dict": IPA_LICENCE, "moby-hyphenator": MOBY_LICENCE},
        "note": (
            "ipa-dict is MIT and may be redistributed with attribution; the "
            "Moby hyphenator is public domain in the US. The index is built "
            "beside the user's word file rather than committed, so the "
            "repository carries neither source."
        ),
        "normalisation": (
            "Stress marks are moved to the front of their syllable; every other "
            "symbol is exactly as the dictionary wrote it."
        ),
        "inputs": {
            name: {"url": IPA_SOURCES[name], "sha256": sha256(p), "bytes": p.stat().st_size}
            for name, p in local.items()
        } | {"moby-hyphenator": {"url": MOBY_URL, "sha256": sha256(moby_path), "bytes": moby_path.stat().st_size}},
        "outputs": {
            "phonics.tsv": len(rows),
            "skipped_no_chunks_or_count_mismatch": skipped,
            "words_without_hyphenation": no_moby,
            "spelling_vs_sound_count_mismatch": mismatched,
        },
    }
    (out / "sources.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    print(f"  written to {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

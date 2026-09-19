#!/usr/bin/env python3
"""Report how the built phonics index does against the hand-checked answer key.

The Go test `TestPhonicsGoldSet` runs the same comparison through the reader the
tool actually uses. This script exists for the other direction: when a rule
changes, it prints every miss with both readings, which is what makes it obvious
whether the change helped or merely moved the mistakes around.

Usage:
    python3 scripts/check_phonics.py                       # uses ~/.ewh/lexicon
    python3 scripts/check_phonics.py --index /tmp/phonics.tsv
    python3 scripts/check_phonics.py --index DIR/phonics.tsv --gold path.tsv
"""

import argparse
import os
import pathlib
import sys


def load_index(path):
    table = {}
    with open(path, encoding="utf-8") as f:
        for line in f:
            fields = line.rstrip("\n").split("\t")
            if len(fields) >= 5:
                table[fields[0]] = fields
    return table


def load_gold(path):
    entries = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            fields = line.split("\t")
            if len(fields) >= 2:
                entries.append((fields[0], fields[1].strip()))
    return entries


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--index", default=os.path.join(os.path.expanduser("~/.ewh/lexicon"), "phonics.tsv"))
    ap.add_argument("--gold", default="internal/lexicon/testdata/phonics_gold.tsv")
    args = ap.parse_args()

    index_path = pathlib.Path(args.index)
    if not index_path.exists():
        print(f"no index at {index_path}; run scripts/build_phonics.py first", file=sys.stderr)
        return 1
    index = load_index(index_path)
    gold = load_gold(args.gold)
    if not gold:
        print(f"no answer key at {args.gold}", file=sys.stderr)
        return 1

    wrong, missing, correct = [], [], 0
    for word, want in gold:
        row = index.get(word)
        if not row:
            missing.append(word)
            continue
        # Compare the chunks themselves, so the answer key may write them either
        # with one pair of slashes per chunk (what the tool prints) or with a
        # single pair around the whole transcription (what the index stores).
        got = [c.strip("/") for c in row[2].split("|")]
        expected = [c.strip("/") for c in want.split("|")]
        want = "|".join(expected)
        if got == expected:
            correct += 1
        else:
            wrong.append((word, want, "|".join(got), row[3]))

    total = len(gold)
    print(f"answer key: {total} words, {correct} correct ({100 * correct / total:.0f}%)")
    if missing:
        print(f"\nnot in the index ({len(missing)}): {', '.join(missing)}")
    if wrong:
        print(f"\nwrong ({len(wrong)}):")
        for word, want, got, spelling in wrong:
            print(f"  {word} ({spelling})")
            print(f"      got  {got.replace('|', ' · ')}")
            print(f"      want {want.replace('|', ' · ')}")

    # A second, automated signal: how often the sound syllables agree in count
    # with the hyphenation source. It cannot say a split is right, but a sudden
    # drop means a rule change broke something broadly.
    total_moby = same = 0
    for row in index.values():
        if "+moby" not in row[4] or not row[3]:
            continue
        total_moby += 1
        if len(row[2].strip("/").split("|")) == len(row[3].split("·")):
            same += 1
    if total_moby:
        print(f"\ncount agrees with hyphenation: {same}/{total_moby} = {100 * same / total_moby:.1f}%")
        print("(the rest are words like comfortable, where the spelling has a syllable the pronunciation does not)")
    return 0 if not wrong and not missing else 1


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""Build the morphology data layer that `love` reads at run time.

The tool's own promise is that a cache hit is offline and free, so nothing here
runs during a lookup: this script is the one place that touches the network, and
it is run by hand (or by a maintainer) to produce small local indexes.

What it builds, in the target directory (default ~/.ewh/lexicon):

    inflections.tsv   form <TAB> lemma <TAB> inflection morpheme
    derivations.tsv   target <TAB> source <TAB> morpheme <TAB> type
    lemmas.txt        one lemma per line, for "is this a word" checks
    sources.json      provenance: where the data came from and under what licence
    README.md         the same, in prose, next to the data

Why MorphyNet and not Wiktionary directly
-----------------------------------------
Wiktionary is the authority, but its own dump is 3.0 GB and the API answers one
word per request at one to five seconds each. MorphyNet is the same Wiktionary
data, already parsed into morpheme segmentations, in 30 MB, and it carries the
two facts this tool actually needs:

    infusing   -> infuse | ing          (an inflected form is not a lemma)
    censorship -> censor  + ship suffix (a derived word has a parent)

Both matter. Etymologies live on the lemma, so `infusing` has no etymology on
Wiktionary at all; and a word's parts are the chain of its derivations, not a
single prefix/suffix pair.

Usage:
    python3 scripts/build_morphology.py                 # build into ~/.ewh/lexicon
    python3 scripts/build_morphology.py --out DIR       # somewhere else
    python3 scripts/build_morphology.py --force         # re-download the sources

The source files are cached beside the output under `_sources/`, so a rebuild
after the first one is offline and instant.
"""

import argparse
import datetime
import hashlib
import json
import os
import pathlib
import sys
import urllib.request

SOURCE_BASE = "https://raw.githubusercontent.com/kbatsuren/MorphyNet/master/eng"
SOURCES = {
    "eng.derivational.v1.tsv": f"{SOURCE_BASE}/eng.derivational.v1.tsv",
    "eng.inflectional.v1.tsv": f"{SOURCE_BASE}/eng.inflectional.v1.tsv",
}
LICENCE = "CC BY-SA 3.0"
CITATION = (
    "Khuyagbaatar Batsuren, Gábor Bella, Fausto Giunchiglia - "
    "MorphyNet: a Large Multilingual Database of Derivational and Inflectional "
    "Morphology (SIGMORPHON 2021), https://aclanthology.org/2021.sigmorphon-1.5/"
)
UPSTREAM = "https://github.com/kbatsuren/MorphyNet"

# Morpheme types this tool can explain. MorphyNet also records infixes and
# interfixes for other languages; English derivations are prefixes and suffixes.
KEPT_TYPES = {"prefix", "suffix"}

# A derivation whose parent is a proper noun is almost always noise: the pair
# "Curie -> curium" is real morphology, but "curium -> curious" is a Wiktionary
# error that a learner must never be shown. Dropping capitalised parents removes
# that whole family of mistakes without touching the real chains.
def is_noise(source, target, morpheme):
    if not source or not target or not morpheme:
        return True
    if source[0].isupper():
        return True
    if source == target:
        return True
    # A morpheme that is the whole word is not a morpheme.
    if morpheme.lower() == target.lower() or morpheme.lower() == source.lower():
        return True
    if len(morpheme) > 12:
        return True
    return False


def download(url, dest, force=False):
    if dest.exists() and not force and dest.stat().st_size > 0:
        return dest
    dest.parent.mkdir(parents=True, exist_ok=True)
    print(f"  downloading {url}")
    tmp = dest.with_suffix(dest.suffix + ".part")
    with urllib.request.urlopen(url, timeout=120) as response:
        tmp.write_bytes(response.read())
    tmp.replace(dest)
    return dest


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def read_rows(path):
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.rstrip("\n")
            if line:
                yield line.split("\t")


def build_inflections(path):
    """form -> (lemma, inflectional morpheme).

    Only rows whose segmentation actually names a morpheme are kept: MorphyNet
    writes "-" for "this form is the lemma itself", and a lemma needs no entry
    pointing at itself.
    """
    rows = {}
    for parts in read_rows(path):
        if len(parts) < 4:
            continue
        lemma, form, _features, segmentation = parts[0], parts[1], parts[2], parts[3]
        pieces = segmentation.split("|")
        if len(pieces) < 2 or pieces[-1] in ("", "-"):
            continue
        morpheme = pieces[-1]
        # One form can carry several readings (noun plural and verb 3sg both
        # end in -s). The first is enough: the tool needs the lemma and the
        # surface morpheme, and both readings agree on those.
        rows.setdefault(form, (lemma, morpheme))
    return rows


def build_derivations(path):
    """target -> list of (source, morpheme, type), noise removed."""
    rows = {}
    seen = set()
    for parts in read_rows(path):
        if len(parts) < 6:
            continue
        source, target, _spos, _tpos, morpheme, kind = parts[:6]
        if kind not in KEPT_TYPES:
            continue
        if is_noise(source, target, morpheme):
            continue
        key = (target, source, morpheme, kind)
        if key in seen:
            continue
        seen.add(key)
        rows.setdefault(target, []).append((source, morpheme, kind))
    for target in rows:
        rows[target].sort()
    return rows


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--out", default=os.path.join(os.path.expanduser("~/.ewh"), "lexicon"))
    ap.add_argument("--force", action="store_true", help="re-download the source files")
    args = ap.parse_args()

    out = pathlib.Path(args.out)
    cache = out / "_sources"
    out.mkdir(parents=True, exist_ok=True)

    print("fetching MorphyNet (Wiktionary-derived morpheme segmentations)")
    local = {name: download(url, cache / name, args.force) for name, url in SOURCES.items()}

    print("building inflections")
    inflections = build_inflections(local["eng.inflectional.v1.tsv"])
    with open(out / "inflections.tsv", "w", encoding="utf-8") as f:
        for form in sorted(inflections):
            lemma, morpheme = inflections[form]
            f.write(f"{form}\t{lemma}\t{morpheme}\n")

    print("building derivations")
    derivations = build_derivations(local["eng.derivational.v1.tsv"])
    with open(out / "derivations.tsv", "w", encoding="utf-8") as f:
        for target in sorted(derivations):
            for source, morpheme, kind in derivations[target]:
                f.write(f"{target}\t{source}\t{morpheme}\t{kind}\n")

    lemmas = {lemma for lemma, _ in inflections.values()}
    lemmas |= set(derivations)
    lemmas |= {source for rows in derivations.values() for source, _, _ in rows}
    with open(out / "lemmas.txt", "w", encoding="utf-8") as f:
        f.write("\n".join(sorted(lemmas)) + "\n")

    manifest = {
        "built_at": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds"),
        "script": "scripts/build_morphology.py",
        "upstream": UPSTREAM,
        "licence": LICENCE,
        "citation": CITATION,
        "note": (
            "These files are derived from Wiktionary via MorphyNet and are "
            "therefore licensed CC BY-SA 3.0, separately from the MIT-licensed "
            "code that reads them. Keep them beside the user's word file, not "
            "inside the binary, and keep this attribution with them."
        ),
        "inputs": {
            name: {"url": SOURCES[name], "sha256": sha256(path), "bytes": path.stat().st_size}
            for name, path in local.items()
        },
        "outputs": {
            "inflections.tsv": len(inflections),
            "derivations.tsv": sum(len(v) for v in derivations.values()),
            "lemmas.txt": len(lemmas),
        },
    }
    (out / "sources.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    (out / "README.md").write_text(
        "# morphology data\n\n"
        f"Derived from Wiktionary through [MorphyNet]({UPSTREAM}) and licensed\n"
        f"**{LICENCE}**, separately from the MIT-licensed code that reads it.\n\n"
        f"Cite: {CITATION}\n\n"
        f"Rebuild with `python3 scripts/build_morphology.py`, which records the\n"
        f"exact inputs and their hashes in `sources.json`.\n",
        encoding="utf-8",
    )

    print()
    print("verifying the indexes are sorted by key")
    for name, columns in (("inflections.tsv", 3), ("derivations.tsv", 4)):
        if not is_sorted(out / name):
            print(f"  {name} is NOT sorted; lookups would silently miss", file=sys.stderr)
            return 1
        print(f"  {name:<18} sorted")

    for name, count in manifest["outputs"].items():
        print(f"  {name:<18} {count:>8} rows")
    print(f"  written to {out}")
    return 0


def is_sorted(path):
    """Confirm the first column never decreases.

    The Go reader binary-searches these files, so an unsorted file is not slow
    — it is wrong, and wrong in the worst way: a word that is present simply
    never matches, and the tool quietly falls back to the model.
    """
    previous = ""
    with open(path, encoding="utf-8") as f:
        for line in f:
            key = line.split("\t", 1)[0]
            if key < previous:
                return False
            previous = key
    return True


if __name__ == "__main__":
    sys.exit(main())

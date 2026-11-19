#!/usr/bin/env python3
"""Mechanical consistency checks for the project docs.

Checks docs/reference/architecture.md in depth (labels, decision boxes, the decision index, use case
coverage, quality targets) and docs/reference/contributing.md (engineering decisions and their fields),
each with its pair for people (ARCHITECTURE.md, CONTRIBUTING.md), every relative link in the tracked
docs, the labels those docs cite, and plain ASCII.

Run it from any folder: python3 scripts/lib/check_docs.py
Exits with status 1 and lists every problem found.
With --fix-index, first rebuilds the decision index (Appendix C) from the decisions, in reading order.
Labels are never renumbered: each keeps the number it was created with.
"""
import os
import re
import sys
from pathlib import Path

# Every path below is relative to the repository root, found from this file's own place in it, so
# the check reads the same docs whatever folder it is run from.
os.chdir(Path(__file__).resolve().parents[2])

DESIGN = Path("docs/reference/architecture.md")
STANDARDS = Path("docs/reference/contributing.md")
# Each doc for people at the root and its detailed twin in docs/reference/, which agents work
# from: the same top-level sections in the same order, appendices only in the reference.
PAIRS = [(Path("ARCHITECTURE.md"), DESIGN), (Path("CONTRIBUTING.md"), STANDARDS)]
# Working notes kept outside git (CLAUDE.local.md routes to them); absent in a clean clone.
LOCAL_NOTES = Path("local")

# Tracked docs, checked for links and plain ASCII.
TRACKED = [
    Path("README.md"),
    Path("CLAUDE.md"),
    *(human for human, _ in PAIRS),
    *sorted(Path("docs").rglob("*.md")),
    *sorted(Path(".claude/skills").glob("*/SKILL.md")),
    *sorted(Path(".claude/agents").glob("*.md")),
]
# Docs that cite labels from the design. A working note that keeps old labels on purpose opts
# out with a "docs-check: skip" line.
CITING = [
    *(doc for doc in TRACKED if doc != DESIGN),
    *sorted(
        note
        for note in LOCAL_NOTES.rglob("*.md")
        if "docs-check: skip" not in note.read_text(encoding="utf-8")
    ),
]

# Label kinds defined in the design: use cases of users (U), members (W) and the team (O),
# failure families (F), qualities (NFR), assumptions (A), decisions (D); and in the standards:
# engineering decisions (E).
KINDS = "NFR|U|W|O|F|A|D|E"
USE_CASES = "U|W|O|F"

# Facts with a single home. Each pattern may only appear inside the block that starts at the
# named anchor (the block runs until the next anchor). Empty until a fact needs one home.
CANONICAL = {}

problems = []


def problem(path, line, message):
    problems.append(f"{path}:{line}: {message}")


def lines_outside_code(text):
    """Yield (line number, line) for lines outside fenced code blocks."""
    in_code = False
    for number, line in enumerate(text.split("\n"), 1):
        if line.startswith("```"):
            in_code = not in_code
            continue
        if not in_code:
            yield number, line


def anchors(text):
    return re.findall(r'<a id="([^"]+)"></a>', text)


def check_links(path, text):
    """Links inside the doc and to other files: the file exists, and an anchor in a checked doc exists."""
    ids = anchors(text)
    for duplicate in sorted({i for i in ids if ids.count(i) > 1}):
        problem(path, 0, f'anchor "{duplicate}" defined more than once')
    for number, line in lines_outside_code(text):
        for target in re.findall(r"\]\(([^)\s]+)\)", line):
            if re.match(r"[a-z]+:", target):
                continue
            file, _, anchor = target.partition("#")
            resolved = (path.parent / file).resolve() if file else path.resolve()
            if not resolved.exists():
                problem(path, number, f"broken link {target}: no such file")
            elif anchor and resolved.suffix == ".md" and anchor not in anchors(resolved.read_text(encoding="utf-8")):
                problem(path, number, f"broken link {target}: no such anchor")


def defined_labels(design, standards):
    labels = {}
    for kind, number in re.findall(r'<a id="(' + KINDS.lower() + r')(\d+)"></a>', design):
        labels.setdefault(kind.upper(), []).append(number)
    labels["E"] = re.findall(r'<a id="e(\d+)"></a>', standards)
    return labels


def check_numbering(labels):
    """Each label defined once; numbers 1..n with no gaps, in any order (labels keep their creation number)."""
    for kind, numbers in labels.items():
        values = sorted(int(n) for n in numbers)
        if values != list(range(1, len(values) + 1)):
            home = STANDARDS if kind == "E" else DESIGN
            problem(home, 0, f"{kind} labels not numbered 1..n without gaps or repeats: {values}")


def check_mentions_linked(path, text):
    for number, line in lines_outside_code(text):
        if line.lstrip("> ").startswith("#"):
            continue
        stripped = re.sub(r'<a id="[^"]+"></a>(' + KINDS + r")\d+", "", line)
        for match in re.finditer(r"(?<![\[#\w/-])((?:" + KINDS + r")\d+)\b(?!\])", stripped):
            problem(path, number, f"{match.group()} mentioned without a link")


def check_canonical(path, text):
    block = ""
    for number, line in lines_outside_code(text):
        found = re.findall(r'<a id="([^"]+)"></a>', line)
        if found and line.startswith("<a id="):
            block = found[-1]
        for pattern, homes in CANONICAL.items():
            if re.search(pattern, line) and block not in homes:
                problem(path, number, f'"{pattern}" outside its home ({", ".join(sorted(homes))})')


def decisions_in_reading_order(text):
    found = []
    for match in re.finditer(r'<a id="d(\d+)"></a>\n> #### D(\d+)\. ([^\n]+)|^\| <a id="d(\d+)"></a>D(\d+) \| ([^|]+) \|', text, re.M):
        anchor, label, title = (match.group(1), match.group(2), match.group(3)) if match.group(1) else (
            match.group(4), match.group(5), match.group(6))
        if anchor != label:
            problem(DESIGN, 0, f"decision anchor d{anchor} is labelled D{label}")
        found.append((int(anchor), title.strip()))
    return found


def check_decision_index(text):
    head = text[: text.index('<a id="appendix-c"></a>')]
    index = re.findall(r"^\| \[D(\d+)\]\(#d\d+\) \| ([^|]+) \|", text, re.M)
    if [(int(n), t.strip()) for n, t in index] != decisions_in_reading_order(head):
        problem(DESIGN, 0, "decision index (Appendix C) does not match the decisions in reading order; run with --fix-index")
    # --fix-index can't know where a new decision lives, so it leaves a placeholder to replace.
    for number in re.findall(r"^\| \[D(\d+)\]\(#d\d+\) \|.*TODO: section link", text, re.M):
        problem(DESIGN, 0, f"decision index row for D{number} has no section link; replace its TODO")


FIELDS = ["Decision", "Needed by", "Assumes", "Why", "Trade-off", "Alternatives rejected", "Status", "Enforced by"]
REQUIRED = set(FIELDS) - {"Trade-off"}


def check_decision_fields(text):
    for match in re.finditer(r'<a id="(d\d+)"></a>\n((?:>.*\n)+)', text):
        fields = re.findall(r"^> - ([A-Z][\w -]+?):", match.group(2), re.M)
        if not REQUIRED <= set(fields) or fields != sorted(fields, key=FIELDS.index):
            problem(DESIGN, 0, f"{match.group(1).upper()} fields missing or out of order: {fields}")


E_FIELDS = ["Decision", "Needed by", "Why", "Rejected", "Status", "Enforced by", "Revisit"]
E_REQUIRED = set(E_FIELDS) - {"Revisit"}


def check_engineering_fields(text):
    for match in re.finditer(r'<a id="(e\d+)"></a>\n#### E\d+\. [^\n]+\n((?:- .*\n?)+)', text):
        fields = re.findall(r"^- ([A-Z][\w -]+?):", match.group(2), re.M)
        if not E_REQUIRED <= set(fields) or fields != sorted(fields, key=E_FIELDS.index):
            problem(STANDARDS, 0, f"{match.group(1).upper()} fields missing or out of order: {fields}")


def check_coverage(text):
    """Every use case defined in a feature is listed in section 4.1, and every quality has a target."""
    defined = re.findall(r'^- <a id="((?:' + USE_CASES.lower() + r')\d+)"></a>', text, re.M)
    listed = re.findall(r"^\| \[[^\]]+\]\(#[^)]+\) \| \[(?:" + USE_CASES + r")\d+\]\(#((?:" + USE_CASES.lower() + r")\d+)\) ", text, re.M)
    if sorted(defined) != sorted(listed):
        problem(DESIGN, 0, f"use cases in the features and in section 4.1 differ: {sorted(set(defined) ^ set(listed))}")
    for nfr in re.findall(r'<a id="(nfr\d+)"></a>', text):
        if not re.search(r"^\| \[" + nfr.upper() + r" [^\]]+\]\(#" + nfr + r"\) \|", text, re.M):
            problem(DESIGN, 0, f"{nfr.upper()} has no target in any Qualities table")


def check_citations(labels):
    """Every label and design section cited outside the design must exist."""
    sections = set(re.findall(r"^#{2,3} (\d+(?:\.\d+)?)\.? ", DESIGN.read_text(encoding="utf-8"), re.M))
    standards_sections = set(re.findall(r"^## (\d+)\. ", STANDARDS.read_text(encoding="utf-8"), re.M))
    for path in CITING:
        for number, line in enumerate(path.read_text(encoding="utf-8").split("\n"), 1):
            for kind, n in re.findall(r"\b(" + KINDS + r")(\d+)\b", line):
                if n not in labels.get(kind, []):
                    problem(path, number, f"{kind}{n} is not defined in the {'standards' if kind == 'E' else 'design'}")
            for n in re.findall(r"design(?:\.md)?,? sections? (\d+(?:\.\d+)?)", line):
                if n not in sections:
                    problem(path, number, f"design section {n} does not exist")
            for n in re.findall(r"standards,? sections? (\d+)", line):
                if n not in standards_sections:
                    problem(path, number, f"standards section {n} does not exist")


def sections(text):
    return [h for h in re.findall(r"^## (.+)$", text, re.M) if not h.startswith("Appendix")]


def check_pairs():
    for human, reference in PAIRS:
        text = human.read_text(encoding="utf-8")
        if sections(text) != sections(reference.read_text(encoding="utf-8")):
            problem(human, 0, f"top-level sections differ from {reference}'s")
        check_mermaid(human, text)
        if "```mermaid" in reference.read_text(encoding="utf-8"):
            problem(reference, 0, f"diagram in a reference: diagrams live in {human}")


def check_mermaid(path, text):
    """A semicolon ends a Mermaid statement, so one inside a label breaks the diagram."""
    in_diagram = False
    for number, line in enumerate(text.split("\n"), 1):
        if line.startswith("```"):
            in_diagram = line.startswith("```mermaid")
            continue
        if in_diagram and ";" in line and not line.strip().startswith("%%"):
            problem(path, number, "semicolon inside a Mermaid diagram (it ends the statement early)")


def rebuild_index():
    """Rewrite Appendix C from the decision boxes and technology rows, in reading order."""
    text = DESIGN.read_text(encoding="utf-8")
    start = text.index('<a id="appendix-c"></a>')
    head, old = text[:start], text[start:]
    where = dict(re.findall(r"^\| \[D(\d+)\]\(#d\d+\) \| [^|]+ \| (.+) \|$", old, re.M))
    lines = [
        '<a id="appendix-c"></a>',
        "## Appendix C. Decision index",
        "",
        "In reading order; labels are kept when decisions are added later.",
        "",
        "| ID | Decision | Where |",
        "|---|---|---|",
    ]
    for number, title in decisions_in_reading_order(head):
        lines.append(f"| [D{number}](#d{number}) | {title} | {where.get(str(number), 'TODO: section link')} |")
    DESIGN.write_text(head + "\n".join(lines) + "\n", encoding="utf-8")


def main():
    if "--fix-index" in sys.argv:
        rebuild_index()
    design = DESIGN.read_text(encoding="utf-8")
    standards = STANDARDS.read_text(encoding="utf-8")
    labels = defined_labels(design, standards)

    for path in TRACKED:
        check_links(path, path.read_text(encoding="utf-8"))
    check_numbering(labels)
    check_mentions_linked(DESIGN, design)
    check_mentions_linked(STANDARDS, standards)
    check_engineering_fields(standards)
    check_canonical(DESIGN, design)
    check_decision_index(design)
    check_decision_fields(design)
    check_coverage(design)
    check_citations(labels)
    check_pairs()

    for path in TRACKED:
        for number, line in enumerate(path.read_text(encoding="utf-8").split("\n"), 1):
            if re.search(r"[^\x00-\x7F]", line):
                problem(path, number, "non-ASCII character")

    if problems:
        print("\n".join(problems))
        sys.exit(1)
    print("docs: all checks passed")


if __name__ == "__main__":
    main()

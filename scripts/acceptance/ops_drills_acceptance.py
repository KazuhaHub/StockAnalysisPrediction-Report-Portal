#!/usr/bin/env python3
"""Generate the committed OPS-05/OPS-06 acceptance JSON from a run's evidence artifact.

    gh run download RUN -n ops-deployment-drills-evidence -D DIR
    python3 scripts/acceptance/ops_drills_acceptance.py --run RUN --artifact-dir DIR --out FILE

The artifact's id and digest are read from the Actions API, so the committed file names exactly the bytes it
was generated from; nothing in it is typed by hand.
"""
import argparse
import json
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import ops_deploy_drills as drills  # noqa: E402


def artifact_record(run_id, name="ops-deployment-drills-evidence"):
    out = subprocess.run(["gh", "api", f"repos/{drills.REPOSITORY}/actions/runs/{run_id}/artifacts"],
                         capture_output=True, text=True, check=True).stdout
    found = [a for a in json.loads(out)["artifacts"] if a["name"] == name and not a.get("expired")]
    if len(found) != 1:
        raise SystemExit(f"expected one live artifact named {name} on run {run_id}, found {len(found)}")
    return found[0]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--run", required=True)
    parser.add_argument("--artifact-dir", required=True)
    parser.add_argument("--out", required=True)
    args = parser.parse_args(argv)
    docs = {}
    for row in ("ops05", "ops06"):
        with open(os.path.join(args.artifact_dir, f"{row}-evidence.json"), encoding="utf-8") as f:
            docs[row] = json.load(f)
    run_url = f"https://github.com/{drills.REPOSITORY}/actions/runs/{args.run}"
    doc = drills.build_acceptance(docs["ops05"], docs["ops06"], artifact_record(args.run), run_url)
    with open(args.out, "w", encoding="utf-8") as f:
        json.dump(doc, f, indent=2, ensure_ascii=False)
        f.write("\n")


if __name__ == "__main__":
    main()

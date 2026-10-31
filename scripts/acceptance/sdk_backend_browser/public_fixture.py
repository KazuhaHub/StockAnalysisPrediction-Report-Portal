"""Expose only public browser fixture bytes to the non-root acceptance process."""
import argparse
import json
import os
from pathlib import Path
import shutil
import stat
import tempfile

FORMAT = "portal.sdk.browser.fixture/1"


def remove_owned_tree(root):
    root.chmod(0o700)
    for path in root.rglob("*"):
        if path.is_dir():
            path.chmod(0o700)
    shutil.rmtree(root)


def prepare(build, packages, browsers, runner):
    root = Path(tempfile.mkdtemp(prefix="portal-sdk-browser-", dir="/tmp"))
    try:
        shutil.copy2(build / "frontend.zip", root / "frontend.zip")
        shutil.copytree(build / "host", root / "host")
        shutil.copy2(runner, root / "run.cjs")
        for name in ("playwright", "playwright-core"):
            shutil.copytree(packages / name, root / "node_modules" / name)
        shutil.copytree(browsers, root / "browsers")
        (root / "package.json").write_text('{"name":"sdk-browser-fixture","private":true}\n')
        (root / "fixture.json").write_text(json.dumps({"format": FORMAT}) + "\n")
        # No repository, credentials or personal directory is copied. Runtime
        # files are readable/executable but never writable by controller UID.
        for path in sorted(root.rglob("*"), reverse=True):
            mode = path.stat().st_mode
            path.chmod(0o555 if path.is_dir() or mode & 0o111 else 0o444)
        root.chmod(0o755)
        return root
    except BaseException:
        remove_owned_tree(root)
        raise


def cleanup(root):
    info = root.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or root.parent != Path("/tmp") or not root.name.startswith("portal-sdk-browser-"):
        raise ValueError("foreign SDK fixture cleanup refused")
    if json.loads((root / "fixture.json").read_text()) != {"format": FORMAT}:
        raise ValueError("SDK fixture ownership marker missing")
    # Only this known owned tree is made owner-writable for its removal.
    remove_owned_tree(root)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cleanup", type=Path)
    parser.add_argument("--build", type=Path)
    parser.add_argument("--packages", type=Path)
    parser.add_argument("--browsers", type=Path)
    parser.add_argument("--runner", type=Path)
    args = parser.parse_args()
    if args.cleanup:
        cleanup(args.cleanup)
    else:
        if not all((args.build, args.packages, args.browsers, args.runner)):
            parser.error("complete public fixture inputs required")
        print(prepare(args.build, args.packages, args.browsers, args.runner))

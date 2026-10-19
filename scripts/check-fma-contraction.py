#!/usr/bin/env python3
"""Fail if the compiler contracted a multiply-add into an FMA in the arch-sensitive arithmetic.

    ./scripts/check-fma-contraction.py

Go permits contracting `a*b + c` into a single fused multiply-add, and whether it does depends on the
target architecture: arm64 and amd64 built for GOAMD64>=v3 contract, the base (SSE2) amd64 build does
not, and the two round differently at the last bit. The forecast statistics' p-values and the run
queue's priority score must be identical on every build of the multi-arch release image — a "mature
cell can never change" promise is only true if two architectures agree on the same ledger — so their
multiply-adds carry an explicit float64() barrier (see forecast_groups.go / forecast_stats.go /
queue/priority.go).

That barrier's whole effect is invisible in normal test output: on base amd64 it is a no-op, so a test
run on the amd64 CI cannot see it being removed. This check compiles the two packages for arm64 and
reads the disassembly for FMADD/FMSUB/FNMADD/FNMSUB in the in-scope functions, which catches the
regression on ANY host.

The one sanctioned exception is averageRanks (forecast_stats.go): its `(i+j-1)/2 + 1` becomes
`*0.5 + 1`, and a multiply by a power of two is exact, so the contract there cannot change the result.
"""
import os
import re
import subprocess
import sys

# The packages whose float arithmetic must be arch-stable, and the functions whose FMA is sanctioned.
PACKAGES = ["./internal/app", "./internal/queue"]
IN_SCOPE_FILES = re.compile(r"/internal/app/forecast_[a-z_]*\.go$|/internal/queue/[a-z_]*\.go$")
EXCEPT_FUNCS = {"averageRanks"}

FMA = re.compile(r"\b(FMADD|FMSUB|FNMADD|FNMSUB)[SD]?\b")
STEXT = re.compile(r"^(\S+)\s+STEXT\b")
# e.g. `\t0x0130 00304 (/abs/path/file.go:293)\tFNMSUBD\tF3, F2, F1, F0`
INSTR = re.compile(r"^\s+0x[0-9a-f]+\s+\d+\s+\(([^:]+):(\d+)\)\s+([A-Z0-9.]+)")


def disassemble():
    # Run from the repo root (this file's parent) so `go build` finds the module wherever the caller is.
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    env = {**os.environ, "GOARCH": "arm64", "CGO_ENABLED": "0", "GOWORK": "off", "GOTOOLCHAIN": "local"}
    result = subprocess.run(["go", "build", "-gcflags=-S", *PACKAGES],
                            capture_output=True, text=True, env=env, cwd=root)
    if result.returncode:
        sys.stderr.write(result.stdout + result.stderr)
        raise SystemExit("go build -gcflags=-S failed")
    return result.stdout + result.stderr


def scan(text):
    hits = []
    func = None
    for line in text.splitlines():
        m = STEXT.match(line)
        if m:
            func = m.group(1)
            continue
        m = INSTR.match(line)
        if not m:
            continue
        path, lineno, mnemonic = m.group(1), int(m.group(2)), m.group(3)
        if not FMA.search(mnemonic) or not IN_SCOPE_FILES.search(path):
            continue
        # The unqualified function name, e.g. "...internal/app.pearson" -> "pearson".
        name = func.rsplit(".", 1)[-1] if func else ""
        if name in EXCEPT_FUNCS:
            continue
        hits.append((func, path, lineno, mnemonic))
    return hits


def main():
    hits = scan(disassemble())
    if hits:
        print("FMA contraction found in arch-sensitive arithmetic (arm64 disassembly):", file=sys.stderr)
        for func, path, lineno, mnemonic in hits:
            print(f"  {mnemonic}\t{path}:{lineno}\t{func}", file=sys.stderr)
        print("\nWrap the product in an explicit float64() so the contraction cannot happen, or add the\n"
              "function to EXCEPT_FUNCS here with a reason (a multiply by a power of two is exact).",
              file=sys.stderr)
        return 1
    print("ok: no FMA contraction in the forecast statistics or the queue priority arithmetic")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

"""Unit checks for the FMA-contraction scanner (scripts/check-fma-contraction.py).

The scanner's job is to read `go build -gcflags=-S` output and report FMA instructions in the
arch-sensitive functions. What those functions compile to is a property of the toolchain, so the tests
here exercise the PARSING and the exception rule against canned disassembly, not a live build.
"""
import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import importlib.util

spec = importlib.util.spec_from_file_location(
    "check_fma_contraction", os.path.join(HERE, "check-fma-contraction.py"))
cfc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cfc)


def stext(name):
    return f"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/app.{name} STEXT size=8 align=0x0"


def instr(func_path, line, mnemonic):
    return f"\t0x0100 00128 ({func_path}:{line})\t{mnemonic}\tF0, F1, F2, F0"


SAMPLE = "\n".join([
    stext("pearson"),
    instr("/repo/internal/app/forecast_stats.go", 78, "FMADDD"),
    instr("/repo/internal/app/forecast_stats.go", 79, "VMOVD"),  # not an FMA
    stext("averageRanks"),
    instr("/repo/internal/app/forecast_stats.go", 53, "FMADDD"),
    stext("Widget"),
    instr("/repo/internal/app/some_other.go", 10, "FMADDD"),  # out of scope file
    stext("scoreForecastCell"),
    instr("/repo/internal/app/forecast_groups.go", 218, "FNMSUBD"),
])


class ScanFMATest(unittest.TestCase):
    def test_flags_in_scope_fma(self):
        hits = cfc.scan(SAMPLE)
        got = {(os.path.basename(p), l, m) for _, p, l, m in hits}
        self.assertIn(("forecast_stats.go", 78, "FMADDD"), got)
        self.assertIn(("forecast_groups.go", 218, "FNMSUBD"), got)

    def test_excepts_average_ranks(self):
        funcs = {f.rsplit(".", 1)[-1] for f, _, _, _ in cfc.scan(SAMPLE)}
        self.assertNotIn("averageRanks", funcs)

    def test_ignores_out_of_scope_files(self):
        paths = {p for _, p, _, _ in cfc.scan(SAMPLE)}
        self.assertNotIn("/repo/internal/app/some_other.go", paths)

    def test_ignores_non_fma(self):
        mnem = {m for _, _, _, m in cfc.scan(SAMPLE)}
        self.assertNotIn("VMOVD", mnem)

    def test_clean_input_passes(self):
        clean = "\n".join([stext("pearson"), instr("/repo/internal/app/forecast_stats.go", 78, "FMULDD")])
        self.assertEqual(cfc.scan(clean), [])


if __name__ == "__main__":
    unittest.main()

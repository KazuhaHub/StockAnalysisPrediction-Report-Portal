package app

// One hit predicate (P16a, plan §5.5).
//
// The matrix's hit rate and the drawer's per-row verdict answer the same question: did the stock
// move in the direction the report's upside implied, over and above the benchmark? Keeping the
// answer in one function is what makes the two impossible to disagree.

// The four-state verdict shown per member.
const (
	dirAgree    = "agree"
	dirDisagree = "disagree"
	dirFlat     = "flat"
	dirUntested = "untested"
)

// predHitClass is the single hit predicate. A member is tested only when its implied upside is at
// least predHitUpsideBp; a zero excess is `flat` (neither agreed nor disagreed), and only a sign
// match is agreement.
func predHitClass(terminalFen, s0Fen int64, excessBp int32) string {
	if s0Fen <= 0 {
		return dirUntested
	}
	d := terminalFen - s0Fen
	if abs64(d)*10000 < int64(predHitUpsideBp)*s0Fen {
		return dirUntested
	}
	return hitClassFromSigns(signOfInt64(d), signOfInt32(excessBp))
}

// hitClassFromSigns is the same predicate on precomputed signs, for the permutation loop where the
// prices are not read again.
func hitClassFromSigns(upSign, exSign int8) string {
	switch {
	case exSign == 0:
		return dirFlat
	case (exSign > 0) == (upSign > 0):
		return dirAgree
	default:
		return dirDisagree
	}
}

func signOfInt64(v int64) int8 {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

func signOfInt32(v int32) int8 {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

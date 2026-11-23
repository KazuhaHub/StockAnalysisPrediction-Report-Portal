// Command seccomp_gate is a read-only pre-deploy check. It reads the JSON that `docker inspect <container>`
// prints and reports whether this build recognises the container's seccomp option and policy-sha256 as the
// previous (v1) profile, which the runner then keeps observable, stoppable and removable after an upgrade.
//
//	docker inspect portal-app-<resource id> | go run ./scripts/seccomp_gate
//	go run ./scripts/seccomp_gate inspect.json
//
// It contacts no daemon and changes nothing. Exit status 0 means the container is recognised as v1; 1 means it
// is not (the report says why); 2 is a usage or input error.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: seccomp_gate [inspect.json]  (reads standard input without an argument)")
		return 2
	}
	var input io.Reader = stdin
	if len(args) == 1 {
		file, err := os.Open(args[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		defer file.Close()
		input = file
	}
	raw, err := io.ReadAll(io.LimitReader(input, 4<<20))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	report := driver.InspectSeccompGate(raw)
	encoded, _ := json.MarshalIndent(report, "", "  ")
	fmt.Fprintln(stdout, string(encoded))
	if report.RecognisedAsV1 {
		return 0
	}
	return 1
}

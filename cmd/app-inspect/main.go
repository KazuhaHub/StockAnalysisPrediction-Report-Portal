// Command app-inspect checks v2 descriptor/frontend integrity without installing
// files or executing application code. It does not verify publisher trust.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, output, diagnostics io.Writer) int {
	flags := flag.NewFlagSet("app-inspect", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	descriptorPath := flags.String("descriptor", "", "Path to the v2 descriptor JSON")
	frontendPath := flags.String("frontend", "", "Path to the frontend ZIP artifact")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *descriptorPath == "" || *frontendPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(diagnostics, "Both -descriptor and -frontend are required; positional arguments are not accepted.")
		return 2
	}
	raw, err := readRegularFile(*descriptorPath, appplatform.MaxDescriptorBytes)
	if err != nil {
		fmt.Fprintln(diagnostics, "Descriptor input is unavailable, non-regular or oversized.")
		return 1
	}
	descriptor, err := appplatform.ParseDescriptor(raw)
	if err != nil {
		fmt.Fprintln(diagnostics, "Descriptor declaration is invalid.")
		return 1
	}
	artifact, err := readRegularFile(*frontendPath, appplatform.MaxFrontendArchiveBytes)
	if err != nil {
		fmt.Fprintln(diagnostics, "Frontend input is unavailable, non-regular or oversized.")
		return 1
	}
	inspection, err := appplatform.InspectFrontend(descriptor.Manifest, artifact)
	if err != nil {
		fmt.Fprintln(diagnostics, "Frontend integrity inspection failed.")
		return 1
	}
	execution := "static"
	if descriptor.Manifest.Backend != nil {
		execution = "managed"
	}
	result := struct {
		AppID            string                         `json:"app_id"`
		Version          string                         `json:"version"`
		Publisher        string                         `json:"publisher"`
		DescriptorSHA256 string                         `json:"descriptor_sha256"`
		Execution        string                         `json:"execution"`
		Verification     string                         `json:"verification"`
		Frontend         appplatform.FrontendInspection `json:"frontend"`
	}{descriptor.Manifest.ID, descriptor.Manifest.Version, descriptor.Manifest.Publisher, descriptor.SHA256, execution, "frontend_integrity_only", inspection}
	if err := json.NewEncoder(output).Encode(result); err != nil {
		fmt.Fprintln(diagnostics, "Inspection output could not be written.")
		return 1
	}
	return 0
}

func readRegularFile(name string, limit int64) ([]byte, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, os.ErrInvalid
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	current, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !current.Mode().IsRegular() || !os.SameFile(info, current) || current.Size() > limit {
		return nil, os.ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, os.ErrInvalid
	}
	return raw, nil
}

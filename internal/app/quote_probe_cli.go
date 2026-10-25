package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// QuoteProbeCommand deliberately accepts no configuration path or Store. Captures and recorded
// comparisons are private local files; measurements never enable a source or write runtime state.
func QuoteProbeCommand(ctx context.Context, args []string, output io.Writer) error {
	return quoteProbeCommand(ctx, args, output, defaultQuoteSources(), vendorClient)
}

func quoteProbeCommand(ctx context.Context, args []string, output io.Writer, registry []quoteSource, client *http.Client) error {
	const usage = "usage: report-portal quote-probe capture|compare --help"
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	if args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(output, usage)
		return err
	}
	flags := flag.NewFlagSet("quote-probe "+args[0], flag.ContinueOnError)
	flags.SetOutput(output)
	var result any
	switch args[0] {
	case "capture":
		directory := flags.String("out", "", "new private capture directory (must not exist)")
		from := flags.String("from", "", "inclusive YYYY-MM-DD window start")
		to := flags.String("to", "", "inclusive YYYY-MM-DD window end")
		sources := flags.String("sources", "tencent,sina,yahoo", "comma-separated compiled-in sources; measurement only")
		suspended := flags.String("suspended", "", "additional suspended targets, market:code separated by commas; status is operator-supplied")
		selection := flags.String("targets", "", "explicit market:code targets instead of the fixed panel; cannot combine with --suspended")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("unexpected positional arguments")
		}
		targets := quoteProbeFixedPanel()
		if *selection != "" {
			if *suspended != "" {
				return fmt.Errorf("--targets cannot combine with --suspended")
			}
			targets = nil
			for _, value := range strings.Split(*selection, ",") {
				market, code, ok := strings.Cut(strings.TrimSpace(value), ":")
				if !ok {
					return fmt.Errorf("targets require market:code")
				}
				targets = append(targets, quoteProbeTarget{Market: market, Code: code})
			}
		}
		if *suspended != "" {
			for _, value := range strings.Split(*suspended, ",") {
				market, code, ok := strings.Cut(strings.TrimSpace(value), ":")
				if !ok {
					return fmt.Errorf("suspended targets require market:code")
				}
				targets = append(targets, quoteProbeTarget{Market: market, Code: code})
			}
		}
		names := strings.Split(*sources, ",")
		for i := range names {
			names[i] = strings.TrimSpace(names[i])
		}
		manifest, err := captureQuoteProbe(ctx, quoteProbeCaptureOptions{Directory: *directory, From: *from, To: *to, Sources: names, Targets: targets}, registry, client)
		if err != nil {
			return err
		}
		result = manifest
	case "compare":
		directory := flags.String("in", "", "existing capture directory; replay is offline")
		primary := flags.String("primary", "tencent", "captured reference source (not a settlement grant)")
		start := flags.String("anchor-start", "", "first required primary anchor, YYYY-MM-DD")
		end := flags.String("anchor-end", "", "second required primary anchor, YYYY-MM-DD")
		record := flags.Bool("record", false, "require the full fixed panel and create comparison.json exclusively")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if flags.NArg() != 0 || *directory == "" {
			return fmt.Errorf("compare requires --in and no positional arguments")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		report, err := compareQuoteProbe(*directory, *primary, [2]string{*start, *end})
		if err != nil {
			return err
		}
		if *record {
			if err := recordQuoteProbeReport(*directory, report); err != nil {
				return err
			}
		}
		result = report
	default:
		return fmt.Errorf("%s", usage)
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

// The panel is a reproducible measurement set, not a stock recommendation. Additional suspended
// symbols are supplied by the operator, never inferred from a vendor's missing history.
func quoteProbeFixedPanel() []quoteProbeTarget {
	return []quoteProbeTarget{
		{Market: "sh", Code: "000852"}, {Market: "sh", Code: "000906"}, {Market: "sh", Code: "000300"},
		{Market: "sh", Code: "600000"}, {Market: "sh", Code: "600519"}, {Market: "sh", Code: "600036"},
		{Market: "sh", Code: "601318"}, {Market: "sh", Code: "601899"}, {Market: "sh", Code: "688001"},
		{Market: "sh", Code: "688981"}, {Market: "sh", Code: "689009"},
		{Market: "sz", Code: "000001"}, {Market: "sz", Code: "000333"}, {Market: "sz", Code: "000651"},
		{Market: "sz", Code: "002594"}, {Market: "sz", Code: "002415"}, {Market: "sz", Code: "300750"},
		{Market: "sz", Code: "300059"}, {Market: "sz", Code: "301308"}, {Market: "sz", Code: "301269"},
		{Market: "bj", Code: "830799"}, {Market: "bj", Code: "835185"}, {Market: "bj", Code: "920002"},
	}
}

func recordQuoteProbeReport(directory string, report quoteProbeComparisonReport) error {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	reader := quoteProbeEvidenceReader{root: root, remaining: 8 << 20}
	body, err := reader.read("manifest.json", 8<<20)
	if err != nil {
		return err
	}
	if quoteProbeSHA256(body) != report.ManifestSHA256 {
		return fmt.Errorf("capture manifest changed before recording")
	}
	var manifest quoteProbeManifest
	if err := decodeQuoteProbeJSON(body, &manifest); err != nil {
		return err
	}
	seen := map[quoteProbeTarget]bool{}
	for _, target := range manifest.Targets {
		seen[target] = true
	}
	for _, target := range quoteProbeFixedPanel() {
		if !seen[target] {
			return fmt.Errorf("--record requires full fixed panel; missing %s:%s", target.Market, target.Code)
		}
	}
	body, err = json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return writeQuoteProbeFile(root, "comparison.json", body)
}

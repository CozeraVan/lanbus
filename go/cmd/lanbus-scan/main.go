// lanbus-scan: native Go LAN AI-agent discovery.
// Reads a JSON spec on stdin (or --spec-file) and writes the same report shape
// as lanbus.discovery to stdout.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/CozeraVan/lanbus/go/internal/scanner"
)

func main() {
	specFile := flag.String("spec-file", "", "JSON spec file (default: stdin)")
	version := flag.Bool("version", false, "print version")
	flag.Parse()
	if *version {
		fmt.Println("lanbus-scan 0.1.0")
		return
	}
	var spec map[string]any
	if *specFile != "" {
		raw, err := os.ReadFile(*specFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "lanbus-scan: read spec:", err)
			os.Exit(2)
		}
		if err := json.Unmarshal(raw, &spec); err != nil {
			fmt.Fprintln(os.Stderr, "lanbus-scan: bad spec:", err)
			os.Exit(2)
		}
	} else {
		dec := json.NewDecoder(os.Stdin)
		if err := dec.Decode(&spec); err != nil {
			fmt.Fprintln(os.Stderr, "lanbus-scan: read spec:", err)
			os.Exit(2)
		}
	}
	run(spec)
}

func run(spec map[string]any) {
	rep, err := scanner.Discover(spec)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lanbus-scan:", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rep); err != nil {
		fmt.Fprintln(os.Stderr, "lanbus-scan: encode:", err)
		os.Exit(1)
	}
}

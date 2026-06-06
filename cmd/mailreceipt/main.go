// Command mailreceipt turns a dropped email plus a mail server log into a cited
// delivery receipt: did this message reach the recipient, and if not, why — with
// the verbatim log line as evidence. It is the thing a mail admin does by hand,
// made deterministic and attachable.
//
// main stays thin: it wires flags and delegates to internal packages.
package main

import (
	"fmt"
	"os"

	"github.com/ppiankov/mailreceipt/internal/cli"
)

func main() {
	if err := cli.Root().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "mailreceipt:", err)
		os.Exit(1)
	}
}

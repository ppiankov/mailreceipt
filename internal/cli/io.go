package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/ppiankov/mailreceipt/internal/receipt"
	"github.com/spf13/cobra"
)

// openInput returns a reader for the email: a file when path is non-empty, else
// stdin. The returned closer is always safe to call.
func openInput(path string) (io.Reader, func(), error) {
	if path == "" || path == "-" {
		return os.Stdin, func() {}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, func() {}, fmt.Errorf("opening email %q: %w", path, err)
	}
	return f, func() { f.Close() }, nil
}

// writeReceipt renders the receipt in the requested format to stdout.
func writeReceipt(cmd *cobra.Command, rec receipt.Receipt, format string) error {
	out := cmd.OutOrStdout()
	switch format {
	case "json":
		b, err := rec.JSON()
		if err != nil {
			return err
		}
		fmt.Fprintln(out, string(b))
	case "md", "markdown", "":
		fmt.Fprint(out, rec.Markdown())
	default:
		return fmt.Errorf("unknown --format %q (use md or json)", format)
	}
	return nil
}

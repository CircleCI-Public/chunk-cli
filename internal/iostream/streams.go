package iostream

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// Streams bundles the standard output and error writers.
// Out is for results (data a script would consume).
// Err is for status, progress, and warnings.
type Streams struct {
	Out io.Writer
	Err io.Writer
}

// FromCmd creates Streams from a cobra command's configured writers.
func FromCmd(cmd *cobra.Command) Streams {
	return Streams{
		Out: cmd.OutOrStdout(),
		Err: cmd.ErrOrStderr(),
	}
}

func (s Streams) Println(a ...any)                  { _, _ = fmt.Fprintln(s.Out, a...) }
func (s Streams) Printf(format string, a ...any)    { _, _ = fmt.Fprintf(s.Out, format, a...) }
func (s Streams) ErrPrintln(a ...any)               { _, _ = fmt.Fprintln(s.Err, a...) }
func (s Streams) ErrPrintf(format string, a ...any) { _, _ = fmt.Fprintf(s.Err, format, a...) }

// PrintJSON writes v to w as indented JSON followed by a newline.
func PrintJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

package iostream_test

import (
	"bytes"
	"testing"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/spf13/cobra"
)

func TestStreamsOutput(t *testing.T) {
	tests := []struct {
		name    string
		call    func(iostream.Streams)
		wantOut string
		wantErr string
	}{
		{
			name:    "Println",
			call:    func(s iostream.Streams) { s.Println("hello", "world") },
			wantOut: "hello world\n",
		},
		{
			name:    "Printf",
			call:    func(s iostream.Streams) { s.Printf("count: %d", 42) },
			wantOut: "count: 42",
		},
		{
			name:    "ErrPrintln",
			call:    func(s iostream.Streams) { s.ErrPrintln("warning") },
			wantErr: "warning\n",
		},
		{
			name:    "ErrPrintf",
			call:    func(s iostream.Streams) { s.ErrPrintf("error: %s", "bad") },
			wantErr: "error: bad",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errBuf bytes.Buffer
			s := iostream.Streams{Out: &out, Err: &errBuf}

			tt.call(s)

			if got := out.String(); got != tt.wantOut {
				t.Errorf("Out = %q, want %q", got, tt.wantOut)
			}
			if got := errBuf.String(); got != tt.wantErr {
				t.Errorf("Err = %q, want %q", got, tt.wantErr)
			}
		})
	}
}

func TestFromCmd(t *testing.T) {
	var out, errBuf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)

	s := iostream.FromCmd(cmd)

	s.Println("out")
	s.ErrPrintln("err")

	if out.String() != "out\n" {
		t.Errorf("Out = %q, want %q", out.String(), "out\n")
	}
	if errBuf.String() != "err\n" {
		t.Errorf("Err = %q, want %q", errBuf.String(), "err\n")
	}
}

package iostream_test

import (
	"bytes"
	"testing"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/spf13/cobra"
)

func TestStreams_Println(t *testing.T) {
	var out, errBuf bytes.Buffer
	s := iostream.Streams{Out: &out, Err: &errBuf}

	s.Println("hello", "world")

	got := out.String()
	want := "hello world\n"
	if got != want {
		t.Errorf("Println wrote %q, want %q", got, want)
	}
	if errBuf.Len() != 0 {
		t.Errorf("Println wrote to Err: %q", errBuf.String())
	}
}

func TestStreams_Printf(t *testing.T) {
	var out bytes.Buffer
	s := iostream.Streams{Out: &out}

	s.Printf("count: %d", 42)

	got := out.String()
	want := "count: 42"
	if got != want {
		t.Errorf("Printf wrote %q, want %q", got, want)
	}
}

func TestStreams_ErrPrintln(t *testing.T) {
	var out, errBuf bytes.Buffer
	s := iostream.Streams{Out: &out, Err: &errBuf}

	s.ErrPrintln("warning")

	got := errBuf.String()
	want := "warning\n"
	if got != want {
		t.Errorf("ErrPrintln wrote %q, want %q", got, want)
	}
	if out.Len() != 0 {
		t.Errorf("ErrPrintln wrote to Out: %q", out.String())
	}
}

func TestStreams_ErrPrintf(t *testing.T) {
	var errBuf bytes.Buffer
	s := iostream.Streams{Err: &errBuf}

	s.ErrPrintf("error: %s", "bad")

	got := errBuf.String()
	want := "error: bad"
	if got != want {
		t.Errorf("ErrPrintf wrote %q, want %q", got, want)
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

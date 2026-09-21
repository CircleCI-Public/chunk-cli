package mutate

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestTailBufferKeepsNewestOutput(t *testing.T) {
	buf := newTailBuffer(4)
	_, err := buf.Write([]byte("abc"))
	assert.NilError(t, err)
	_, err = buf.Write([]byte("def"))
	assert.NilError(t, err)

	assert.Equal(t, buf.String(), "[2 bytes truncated]\ncdef")
}

func TestTailBufferBoundsSingleLargeWrite(t *testing.T) {
	buf := newTailBuffer(4)
	_, err := buf.Write([]byte("abcdef"))
	assert.NilError(t, err)

	assert.Equal(t, buf.String(), "[2 bytes truncated]\ncdef")
	assert.Assert(t, !strings.Contains(buf.String(), "ab"))
}

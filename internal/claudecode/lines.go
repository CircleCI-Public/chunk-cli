package claudecode

import "bytes"

// lineSplitter buffers partial writes and calls a callback for each complete
// line found across calls, as stdout arrives mid-line.
type lineSplitter struct {
	buf      []byte
	overflow bool
}

// write calls line for each complete line in data; a line over maxLineBytes is
// dropped rather than buffered.
func (l *lineSplitter) write(data []byte, line func([]byte)) {
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			l.appendPartial(data)
			return
		}
		l.appendPartial(data[:i])
		if !l.overflow {
			line(l.buf)
		}
		l.buf, l.overflow = l.buf[:0], false
		data = data[i+1:]
	}
}

// flush handles a last line that had no trailing newline.
func (l *lineSplitter) flush(line func([]byte)) {
	if len(l.buf) > 0 && !l.overflow {
		line(l.buf)
	}
	l.buf, l.overflow = nil, false
}

func (l *lineSplitter) appendPartial(data []byte) {
	if l.overflow || len(l.buf)+len(data) > maxLineBytes {
		l.overflow = true
		return
	}
	l.buf = append(l.buf, data...)
}

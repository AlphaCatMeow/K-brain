package bashrun

import (
	"fmt"
	"os"
)

const maxOutputBytes = 1 << 20
const maxUpdateBytes = 16 << 10

// Keep a bounded tail; oversized command output remains available on disk.
type outputBuffer struct {
	data     []byte
	total    int64
	file     *os.File
	spillErr error
}

func (b *outputBuffer) Write(p []byte) {
	b.total += int64(len(p))
	if b.file == nil && b.spillErr == nil && len(b.data)+len(p) > maxOutputBytes {
		path := Spill(string(b.data))
		if path == "" {
			b.spillErr = fmt.Errorf("cannot create output log")
		} else {
			b.file, b.spillErr = os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
		}
	}
	if b.file != nil && b.spillErr == nil {
		_, b.spillErr = b.file.Write(p)
	}
	if len(p) >= maxOutputBytes {
		b.data = append(b.data[:0], p[len(p)-maxOutputBytes:]...)
	} else {
		if extra := len(b.data) + len(p) - maxOutputBytes; extra > 0 {
			copy(b.data, b.data[extra:])
			b.data = b.data[:len(b.data)-extra]
		}
		b.data = append(b.data, p...)
	}
}

func (b *outputBuffer) snapshot() string {
	start := max(0, len(b.data)-maxUpdateBytes)
	return string(b.data[start:])
}

func (b *outputBuffer) result() string {
	if b.file != nil {
		if err := b.file.Close(); err != nil && b.spillErr == nil {
			b.spillErr = err
		}
	}
	text := string(b.data)
	if b.total <= int64(len(b.data)) {
		return text
	}
	if b.file != nil && b.spillErr == nil {
		return fmt.Sprintf("[Output truncated; full output: %s]\n%s", b.file.Name(), text)
	}
	return "[Output truncated; full output could not be saved]\n" + text
}

func (b *outputBuffer) path() string {
	if b.file != nil && b.spillErr == nil {
		return b.file.Name()
	}
	return ""
}

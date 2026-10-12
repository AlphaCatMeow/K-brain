package bashrun

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestOutputBufferBoundsMemoryAndPreservesSpill(t *testing.T) {
	var out outputBuffer
	chunk := strings.Repeat("x", 32*1024)
	out.Write([]byte("HEAD"))
	for range 128 {
		out.Write([]byte(chunk))
		if len(out.data) > maxOutputBytes {
			t.Fatal("unbounded output")
		}
	}
	out.Write([]byte("TAIL"))
	result := out.result()
	path := out.path()
	if path == "" {
		t.Fatal("full output was not saved")
	}
	defer os.Remove(path)
	data, err := os.ReadFile(path)
	if err != nil || len(data) != 4<<20+8 || !strings.HasPrefix(string(data), "HEAD") || !strings.HasSuffix(string(data), "TAIL") {
		t.Fatalf("invalid full output: %d %v", len(data), err)
	}
	if !strings.HasSuffix(result, "TAIL") || len(out.snapshot()) > maxUpdateBytes {
		t.Fatal("invalid tail")
	}
}

func TestOutputUpdatesSkipUnchangedContentAndFinishBeforeReturn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	calls := 0
	result := Run(t.Context(), Options{Command: "printf hello; sleep 0.6", OnUpdate: func(s string) {
		calls++
		if s != "hello" {
			t.Errorf("snapshot=%q", s)
		}
	}, Timeout: 5 * time.Second})
	if result.Exit != "" || result.Output != "hello" || calls != 1 {
		t.Fatalf("result=%+v calls=%d", result, calls)
	}
}

func TestLargeCommandOutputSpillsCompleteContent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	result := Run(t.Context(), Options{Command: "printf HEAD; head -c 2097152 /dev/zero; printf TAIL", Timeout: 10 * time.Second})
	if result.OutputFile != "" {
		defer os.Remove(result.OutputFile)
	}
	if result.Exit != "" || result.OutputFile == "" || result.OutputBytes != 2097160 {
		t.Fatalf("exit=%q file=%q bytes=%d", result.Exit, result.OutputFile, result.OutputBytes)
	}
	full, err := os.ReadFile(result.OutputFile)
	if err != nil || len(full) != 2097160 || !strings.HasPrefix(string(full), "HEAD") || !strings.HasSuffix(string(full), "TAIL") {
		t.Fatalf("incomplete spill: bytes=%d err=%v", len(full), err)
	}
	if len(result.Output) > maxOutputBytes+1024 || !strings.HasSuffix(result.Output, "TAIL") {
		t.Fatal("result did not retain bounded tail")
	}
}

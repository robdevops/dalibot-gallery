package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// The integration tests run the real binary. It is built once for all of them.
var (
	builtOnce sync.Once
	builtDir  string
	builtBin  string
	builtErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if builtDir != "" {
		os.RemoveAll(builtDir)
	}
	os.Exit(code)
}

func gallery(t *testing.T) string {
	t.Helper()
	builtOnce.Do(func() {
		builtDir, builtErr = os.MkdirTemp("", "gallery-test-")
		if builtErr != nil {
			return
		}
		builtBin = filepath.Join(builtDir, "gallery")
		if out, err := exec.Command("go", "build", "-o", builtBin, ".").CombinedOutput(); err != nil {
			builtErr = &buildError{err, string(out)}
		}
	})
	if builtErr != nil {
		t.Fatalf("building the gallery: %v", builtErr)
	}
	return builtBin
}

type buildError struct {
	err error
	out string
}

func (e *buildError) Error() string { return e.err.Error() + "\n" + e.out }

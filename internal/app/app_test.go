package app

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPublishedAndDiagnosticWorkExitBeforeTailcatRuntimeClose(t *testing.T) {
	publishedExit := make(chan struct{})
	diagnosticExit := make(chan struct{})
	publisher := closeFunc(func() { close(publishedExit) })
	diagnostics := closeErrorFunc(func() error {
		select {
		case <-publishedExit:
			close(diagnosticExit)
			return nil
		default:
			return errors.New("diagnostics closed before published work exited")
		}
	})
	runtime := closeErrorFunc(func() error {
		select {
		case <-diagnosticExit:
			return nil
		default:
			return errors.New("Tailcat runtime closed before diagnostic work exited")
		}
	})
	if err := closeServicesBeforeTailnet(publisher, diagnostics, runtime); err != nil {
		t.Fatal(err)
	}
}

type closeFunc func()

func (f closeFunc) Close() { f() }

type closeErrorFunc func() error

func (f closeErrorFunc) Close() error { return f() }

func TestDataDirectoryAllowsOnlyOneProcess(t *testing.T) {
	t.Setenv("TAILCAT_WEBUI_DEMO_MODE", "true")
	dataDir := t.TempDir()
	t.Setenv("TAILCAT_WEBUI_DATA_DIR", dataDir)
	t.Setenv("TAILCAT_WEBUI_BASE_URL", "http://localhost:8080")
	t.Setenv("TAILCAT_WEBUI_PUBLISH_BASE_URL", "http://publish.localhost:8080")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first, err := New(t.Context(), logger)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if runtime.GOOS != "windows" {
		for _, path := range []string{dataDir, filepath.Join(dataDir, "tailcat-webui.db"), filepath.Join(dataDir, "tailcat-webui.lock")} {
			info, statErr := os.Stat(path)
			if statErr != nil {
				t.Fatal(statErr)
			}
			want := os.FileMode(0o600)
			if info.IsDir() {
				want = 0o700
			}
			if got := info.Mode().Perm(); got != want {
				t.Fatalf("permissions for %s = %o, want %o", path, got, want)
			}
		}
	}
	if _, err := New(t.Context(), logger); err == nil {
		t.Fatal("second process acquired the same data-directory lock")
	}
}

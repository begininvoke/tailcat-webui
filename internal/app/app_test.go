package app

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestAppOwnsDiagnosticReservedHandlerRegistration(t *testing.T) {
	source, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "RegisterReservedTCPHandler(diagnostics.ReservedPort") {
		t.Fatal("app.go does not register the diagnostics reserved handler")
	}
}

func TestAppSecuresWindowsDataDirectoryBeforeLockAndDatabase(t *testing.T) {
	source, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	secure := strings.Index(text, "privatefs.SecureDataDirectory(cfg.DataDir)")
	lock := strings.Index(text, "flock.New(")
	database := strings.Index(text, "database.Open(")
	if secure < 0 || lock < 0 || database < 0 || secure > lock || secure > database {
		t.Fatalf("private data-directory hardening order secure=%d lock=%d database=%d", secure, lock, database)
	}
}

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

func TestDiagnosticCloseFailureIsSurfacedAfterTailcatClose(t *testing.T) {
	diagnosticErr := errors.New("diagnostic lifecycle unresolved")
	runtimeClosed := false
	err := closeServicesBeforeTailnet(
		closeFunc(func() {}),
		closeErrorFunc(func() error { return diagnosticErr }),
		closeErrorFunc(func() error {
			runtimeClosed = true
			return nil
		}),
	)
	if !errors.Is(err, diagnosticErr) {
		t.Fatalf("close error = %v, want diagnostic failure", err)
	}
	if !runtimeClosed {
		t.Fatal("Tailcat runtime was not closed after diagnostic failure")
	}
}

func TestTransferServiceAndStorageCloseBeforeTailcatRuntime(t *testing.T) {
	order := make([]string, 0, 5)
	err := closeTransferServicesBeforeTailnet(
		closeFunc(func() { order = append(order, "publish") }),
		closeErrorFunc(func() error { order = append(order, "transfer"); return nil }),
		closeErrorFunc(func() error { order = append(order, "storage"); return nil }),
		closeErrorFunc(func() error { order = append(order, "diagnostics"); return nil }),
		closeErrorFunc(func() error { order = append(order, "tailnet"); return nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"publish", "transfer", "storage", "diagnostics", "tailnet"}
	if !slices.Equal(order, want) {
		t.Fatalf("close order = %v, want %v", order, want)
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
		for _, path := range []string{dataDir, filepath.Join(dataDir, "tailcat-webui.db"), filepath.Join(dataDir, "tailcat-webui.lock"), filepath.Join(dataDir, "transfers")} {
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

func TestDemoSecretKeyIsPrivateAndStableAcrossRestart(t *testing.T) {
	directory := t.TempDir()
	first, err := loadOrCreateDemoSecretKey(directory)
	if err != nil {
		t.Fatalf("create demo key: %v", err)
	}
	second, err := loadOrCreateDemoSecretKey(directory)
	if err != nil {
		t.Fatalf("reload demo key: %v", err)
	}
	if !slices.Equal(first, second) || len(first) != 32 {
		t.Fatal("demo key did not remain stable")
	}
	clear(first)
	clear(second)
	if runtime.GOOS != "windows" {
		info, err := os.Lstat(filepath.Join(directory, ".tailcat-webui-demo-key"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("demo key permissions = %o, want 600", info.Mode().Perm())
		}
	}
}

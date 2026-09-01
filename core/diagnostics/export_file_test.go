package diagnostics

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestWriteSupportExportFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "support.json")
	snapshot := BuildSnapshot(time.Unix(123, 0), "SPEED", true, []PathState{{
		Label: " WiFi ", Role: "primary", Status: "up", RTTMillis: 12, TxKbps: 100, RxKbps: 200,
	}})

	if err := WriteSupportExportFile(path, snapshot); err != nil {
		t.Fatalf("WriteSupportExportFile() error = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	want, err := MarshalSupportExport(snapshot)
	if err != nil {
		t.Fatalf("MarshalSupportExport() error = %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("persisted export mismatch\n got: %s\nwant: %s", got, want)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat() error = %v", err)
		}
		if gotPerm := info.Mode().Perm(); gotPerm != 0o600 {
			t.Fatalf("permissions = %o, want 600", gotPerm)
		}
	}
}

func TestWriteSupportExportFileReplacesRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "support.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot := BuildSnapshot(time.Unix(456, 0), "redundant", false, nil)
	if err := WriteSupportExportFile(path, snapshot); err != nil {
		t.Fatalf("WriteSupportExportFile() error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == "old" {
		t.Fatal("destination was not replaced")
	}
}

func TestWriteSupportExportFileRejectsRelativePath(t *testing.T) {
	err := WriteSupportExportFile("support.json", Snapshot{})
	if !errors.Is(err, ErrSupportExportPathRelative) {
		t.Fatalf("error = %v, want ErrSupportExportPathRelative", err)
	}
}

func TestWriteSupportExportFileRejectsDestinationSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated Windows privileges")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	link := filepath.Join(dir, "support.json")
	if err := os.WriteFile(target, []byte("do not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	err := WriteSupportExportFile(link, Snapshot{})
	if !errors.Is(err, ErrSupportExportPathUnsafe) {
		t.Fatalf("error = %v, want ErrSupportExportPathUnsafe", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "do not replace" {
		t.Fatalf("symlink target changed: %q", got)
	}
}

func TestWriteSupportExportFileRejectsSymlinkParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated Windows privileges")
	}
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	linkDir := filepath.Join(root, "link")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}

	err := WriteSupportExportFile(filepath.Join(linkDir, "support.json"), Snapshot{})
	if !errors.Is(err, ErrSupportExportPathUnsafe) {
		t.Fatalf("error = %v, want ErrSupportExportPathUnsafe", err)
	}
	if _, err := os.Stat(filepath.Join(realDir, "support.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected export through symlink parent: %v", err)
	}
}

func TestWriteSupportExportFileRejectsSymlinkedAncestor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated Windows privileges")
	}
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	realNested := filepath.Join(realDir, "nested")
	linkDir := filepath.Join(root, "link")
	if err := os.MkdirAll(realNested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(linkDir, "nested", "support.json")
	err := WriteSupportExportFile(path, Snapshot{})
	if !errors.Is(err, ErrSupportExportPathUnsafe) {
		t.Fatalf("error = %v, want ErrSupportExportPathUnsafe", err)
	}
	if _, err := os.Stat(filepath.Join(realNested, "support.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected export through symlinked ancestor: %v", err)
	}
}

func TestWriteSupportExportFileRejectsNonRegularDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "support.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	err := WriteSupportExportFile(path, Snapshot{})
	if !errors.Is(err, ErrSupportExportPathUnsafe) {
		t.Fatalf("error = %v, want ErrSupportExportPathUnsafe", err)
	}
}

func TestWriteSupportExportFileLeavesNoTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "support.json")
	if err := WriteSupportExportFile(path, Snapshot{}); err != nil {
		t.Fatalf("WriteSupportExportFile() error = %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(dir, ".bondify-support-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files left behind: %v", matches)
	}
}

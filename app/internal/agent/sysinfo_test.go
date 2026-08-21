package agent

import (
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
)

func TestParseOSRelease(t *testing.T) {
	values, err := parseOSRelease(strings.NewReader("# comment\nID=debian\nVERSION_ID=\"12\"\nPRETTY_NAME=\"Debian GNU/Linux 12\"\n"))
	if err != nil {
		t.Fatalf("parse os-release: %v", err)
	}
	if values["ID"] != "debian" || values["VERSION_ID"] != "12" {
		t.Fatalf("unexpected os-release values: %#v", values)
	}
}

func TestCollectLinuxSystemInfoFallsBackWhenOSReleaseIsMissing(t *testing.T) {
	info, err := collectSystemInfo("linux", "amd64", func() (string, error) { return "node", nil }, func() (io.ReadCloser, error) {
		return nil, fs.ErrNotExist
	})
	if err != nil {
		t.Fatalf("collect system info: %v", err)
	}
	if info.Hostname != "node" || info.OS != "linux" || info.OSVersion != "unknown" || info.Arch != "amd64" {
		t.Fatalf("unexpected fallback system info: %#v", info)
	}
}

func TestCollectLinuxSystemInfoReturnsOtherReadErrors(t *testing.T) {
	readError := errors.New("permission denied")
	_, err := collectSystemInfo("linux", "amd64", func() (string, error) { return "node", nil }, func() (io.ReadCloser, error) {
		return nil, readError
	})
	if !errors.Is(err, readError) {
		t.Fatalf("error=%v, want %v", err, readError)
	}
}

func TestCollectLinuxSystemInfoUsesOSRelease(t *testing.T) {
	info, err := collectSystemInfo("linux", "arm64", func() (string, error) { return "node", nil }, func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("ID=debian\nVERSION_ID=12\n")), nil
	})
	if err != nil {
		t.Fatalf("collect system info: %v", err)
	}
	if info.OS != "debian" || info.OSVersion != "12" || info.Arch != "arm64" {
		t.Fatalf("unexpected system info: %#v", info)
	}
}

func TestCollectSystemInfoHasRequiredFields(t *testing.T) {
	info, err := CollectSystemInfo()
	if err != nil {
		t.Fatalf("collect system info: %v", err)
	}
	if info.Hostname == "" || info.OS == "" || info.OSVersion == "" || info.Arch == "" {
		t.Fatalf("incomplete system info: %#v", info)
	}
}

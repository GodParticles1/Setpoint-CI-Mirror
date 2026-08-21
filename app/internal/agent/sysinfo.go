package agent

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"runtime"
	"strings"
)

type SystemInfo struct {
	Hostname  string
	OS        string
	OSVersion string
	Arch      string
}

func CollectSystemInfo() (SystemInfo, error) {
	return collectSystemInfo(runtime.GOOS, runtime.GOARCH, os.Hostname, func() (io.ReadCloser, error) {
		return os.Open("/etc/os-release")
	})
}

func collectSystemInfo(goos, goarch string, hostname func() (string, error), openOSRelease func() (io.ReadCloser, error)) (SystemInfo, error) {
	host, err := hostname()
	if err != nil {
		return SystemInfo{}, fmt.Errorf("read hostname: %w", err)
	}
	info := SystemInfo{Hostname: host, OS: goos, OSVersion: goos, Arch: goarch}
	if goos != "linux" {
		return info, nil
	}
	info.OSVersion = "unknown"
	contents, err := openOSRelease()
	if errors.Is(err, fs.ErrNotExist) {
		return info, nil
	}
	if err != nil {
		return SystemInfo{}, fmt.Errorf("read /etc/os-release: %w", err)
	}
	defer contents.Close()
	values, err := parseOSRelease(contents)
	if err != nil {
		return SystemInfo{}, err
	}
	if value := values["ID"]; value != "" {
		info.OS = value
	}
	if value := values["VERSION_ID"]; value != "" {
		info.OSVersion = value
	} else if value := values["PRETTY_NAME"]; value != "" {
		info.OSVersion = value
	}
	return info, nil
}

func parseOSRelease(source interface{ Read([]byte) (int, error) }) (map[string]string, error) {
	values := make(map[string]string)
	scanner := bufio.NewScanner(source)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "\"")
		values[strings.TrimSpace(key)] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse os-release: %w", err)
	}
	return values, nil
}

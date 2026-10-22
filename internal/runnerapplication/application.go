package runnerapplication

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	OSLinux   = "linux"
	OSWindows = "win"
)

type Application struct {
	// OS is the GitHub Runner package OS: OSLinux or OSWindows. Empty means
	// OSLinux for callers that predate Windows support.
	OS             string
	Architecture   string
	Version        string
	DownloadURL    string
	SHA256Checksum string
}

// FromDescriptor validates a GitHub Runner package descriptor and returns the
// normalized application metadata used at the Sandbox boundary.
func FromDescriptor(os, architecture, filename, downloadURL, checksum string) (Application, error) {
	if err := validateArchitecture(os, architecture); err != nil {
		return Application{}, err
	}
	prefix := "actions-runner-" + os + "-" + architecture + "-"
	suffix := archiveSuffix(os)
	if !strings.HasPrefix(filename, prefix) || !strings.HasSuffix(filename, suffix) {
		return Application{}, fmt.Errorf("github runner application has invalid filename %q", filename)
	}
	version := strings.TrimSuffix(strings.TrimPrefix(filename, prefix), suffix)
	if !validVersion(version) {
		return Application{}, fmt.Errorf("github runner application has invalid version in filename %q", filename)
	}
	return Normalize(Application{
		OS:             os,
		Architecture:   architecture,
		Version:        version,
		DownloadURL:    downloadURL,
		SHA256Checksum: checksum,
	})
}

// Normalize validates application metadata against the official Runner
// release contract and canonicalizes its OS and checksum.
func Normalize(application Application) (Application, error) {
	if application.OS == "" {
		application.OS = OSLinux
	}
	if err := validateArchitecture(application.OS, application.Architecture); err != nil {
		return Application{}, err
	}
	if !validVersion(application.Version) {
		return Application{}, fmt.Errorf("github runner application has invalid version %q", application.Version)
	}
	filename := "actions-runner-" + application.OS + "-" + application.Architecture + "-" + application.Version + archiveSuffix(application.OS)
	wantURL := "https://github.com/actions/runner/releases/download/v" + application.Version + "/" + filename
	if application.DownloadURL != wantURL {
		return Application{}, fmt.Errorf("github runner application has invalid download URL %q", application.DownloadURL)
	}
	checksum := strings.ToLower(strings.TrimSpace(application.SHA256Checksum))
	if len(checksum) != sha256.Size*2 {
		return Application{}, fmt.Errorf("github runner application has invalid SHA-256 checksum")
	}
	if _, err := hex.DecodeString(checksum); err != nil {
		return Application{}, fmt.Errorf("github runner application has invalid SHA-256 checksum: %w", err)
	}
	application.SHA256Checksum = checksum
	return application, nil
}

// SupportedArchitecture reports whether runnerd can install the official
// Runner for os and architecture.
func SupportedArchitecture(os, architecture string) bool {
	return validateArchitecture(os, architecture) == nil
}

func validateArchitecture(os, architecture string) error {
	switch {
	case os == OSLinux && (architecture == "x64" || architecture == "arm64" || architecture == "arm"):
		return nil
	case os == OSWindows && (architecture == "x64" || architecture == "arm64"):
		return nil
	case os != OSLinux && os != OSWindows:
		return fmt.Errorf("github runner application has unsupported OS %q", os)
	default:
		return fmt.Errorf("github runner application has unsupported %s architecture %q", os, architecture)
	}
}

func archiveSuffix(os string) string {
	if os == OSWindows {
		return ".zip"
	}
	return ".tar.gz"
}

func validVersion(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return false
			}
		}
	}
	return true
}

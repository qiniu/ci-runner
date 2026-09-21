package runnerapplication

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

type Application struct {
	Architecture   string
	Version        string
	DownloadURL    string
	SHA256Checksum string
}

// FromDescriptor validates a GitHub Runner package descriptor and returns the
// normalized application metadata used at the Sandbox boundary.
func FromDescriptor(architecture, filename, downloadURL, checksum string) (Application, error) {
	if err := validateArchitecture(architecture); err != nil {
		return Application{}, err
	}
	prefix := "actions-runner-linux-" + architecture + "-"
	const suffix = ".tar.gz"
	if !strings.HasPrefix(filename, prefix) || !strings.HasSuffix(filename, suffix) {
		return Application{}, fmt.Errorf("github runner application has invalid filename %q", filename)
	}
	version := strings.TrimSuffix(strings.TrimPrefix(filename, prefix), suffix)
	if !validVersion(version) {
		return Application{}, fmt.Errorf("github runner application has invalid version in filename %q", filename)
	}
	return Normalize(Application{
		Architecture:   architecture,
		Version:        version,
		DownloadURL:    downloadURL,
		SHA256Checksum: checksum,
	})
}

// Normalize validates application metadata against the official Linux Runner
// release contract and canonicalizes its checksum.
func Normalize(application Application) (Application, error) {
	if err := validateArchitecture(application.Architecture); err != nil {
		return Application{}, err
	}
	if !validVersion(application.Version) {
		return Application{}, fmt.Errorf("github runner application has invalid version %q", application.Version)
	}
	filename := "actions-runner-linux-" + application.Architecture + "-" + application.Version + ".tar.gz"
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

func validateArchitecture(architecture string) error {
	switch architecture {
	case "x64", "arm64", "arm":
		return nil
	default:
		return fmt.Errorf("github runner application has unsupported Linux architecture %q", architecture)
	}
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

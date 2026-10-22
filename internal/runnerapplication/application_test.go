package runnerapplication

import "testing"

func TestFromDescriptorNormalizesValidatedApplication(t *testing.T) {
	application, err := FromDescriptor(
		"linux",
		"x64",
		"actions-runner-linux-x64-2.337.0.tar.gz",
		"https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-linux-x64-2.337.0.tar.gz",
		" 70920811A4F8AD4328818682BCA5C6469C1C942FAB52448868071D0063816613 ",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := Application{
		OS:             "linux",
		Architecture:   "x64",
		Version:        "2.337.0",
		DownloadURL:    "https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-linux-x64-2.337.0.tar.gz",
		SHA256Checksum: "70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613",
	}
	if application != want {
		t.Fatalf("application = %#v, want %#v", application, want)
	}
}

func TestNormalizeRejectsApplicationOutsideOfficialContract(t *testing.T) {
	_, err := Normalize(Application{
		Architecture:   "x64",
		Version:        "2.337.0",
		DownloadURL:    "https://example.com/actions-runner-linux-x64-2.337.0.tar.gz",
		SHA256Checksum: "70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613",
	})
	if err == nil {
		t.Fatal("expected non-official Runner application to be rejected")
	}
}

func TestFromDescriptorAcceptsWindowsZip(t *testing.T) {
	application, err := FromDescriptor(
		"win",
		"x64",
		"actions-runner-win-x64-2.337.0.zip",
		"https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-win-x64-2.337.0.zip",
		"1150692AFA94E71F872017E254EA55B6EECE1EECE3FE7E3A6D4C93D0A1B85CFC",
	)
	if err != nil {
		t.Fatal(err)
	}
	if application.OS != OSWindows || application.Version != "2.337.0" || application.SHA256Checksum != "1150692afa94e71f872017e254ea55b6eece1eece3fe7e3a6d4c93d0a1b85cfc" {
		t.Fatalf("application = %#v", application)
	}
}

func TestNormalizeDefaultsToLinux(t *testing.T) {
	application, err := Normalize(Application{
		Architecture:   "x64",
		Version:        "2.337.0",
		DownloadURL:    "https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-linux-x64-2.337.0.tar.gz",
		SHA256Checksum: "70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613",
	})
	if err != nil {
		t.Fatal(err)
	}
	if application.OS != OSLinux {
		t.Fatalf("OS = %q, want %q", application.OS, OSLinux)
	}
}

func TestFromDescriptorRejectsMismatchedPackages(t *testing.T) {
	const checksum = "70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613"
	tests := []struct {
		name, os, architecture, filename, url string
	}{
		{"windows tarball", "win", "x64", "actions-runner-win-x64-2.337.0.tar.gz", "https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-win-x64-2.337.0.tar.gz"},
		{"linux zip", "linux", "x64", "actions-runner-linux-x64-2.337.0.zip", "https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-linux-x64-2.337.0.zip"},
		{"windows arm", "win", "arm", "actions-runner-win-arm-2.337.0.zip", "https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-win-arm-2.337.0.zip"},
		{"osx", "osx", "x64", "actions-runner-osx-x64-2.337.0.tar.gz", "https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-osx-x64-2.337.0.tar.gz"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := FromDescriptor(test.os, test.architecture, test.filename, test.url, checksum); err == nil {
				t.Fatal("expected package to be rejected")
			}
		})
	}
}

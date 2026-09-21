package runnerapplication

import "testing"

func TestFromDescriptorNormalizesValidatedApplication(t *testing.T) {
	application, err := FromDescriptor(
		"x64",
		"actions-runner-linux-x64-2.337.0.tar.gz",
		"https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-linux-x64-2.337.0.tar.gz",
		" 70920811A4F8AD4328818682BCA5C6469C1C942FAB52448868071D0063816613 ",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := Application{
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

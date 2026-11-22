package install

import "testing"

// TestUsqueAssetURL pins the shape of the download URL. The base used to carry the tag as well and
// archiveSource adds "/v<version>" itself, so the first production switch asked for
// .../download/v4.2.1/v4.2.1/usque_4.2.1_linux_amd64.zip and got a 404.
func TestUsqueAssetURL(t *testing.T) {
	t.Parallel()
	pin, err := usquePin("amd64")
	if err != nil {
		t.Fatalf("usquePin(amd64): %v", err)
	}
	got, remote, err := archiveSource(usqueReleaseBase, usqueVersion, pin.Name)
	if err != nil {
		t.Fatalf("archiveSource: %v", err)
	}
	want := "https://github.com/Diniboy1123/usque/releases/download/v4.2.1/usque_4.2.1_linux_amd64.zip"
	if got != want || !remote {
		t.Errorf("archiveSource = %q, remote %v; want %q, remote true", got, remote, want)
	}
}

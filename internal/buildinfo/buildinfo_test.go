package buildinfo

import "testing"

func TestCurrent(t *testing.T) {
	t.Parallel()

	info := Current()
	if info.Version == "" {
		t.Fatal("Version is empty")
	}
	if info.Go == "" || info.OS == "" || info.Arch == "" {
		t.Fatalf("runtime identity is incomplete: %+v", info)
	}
}

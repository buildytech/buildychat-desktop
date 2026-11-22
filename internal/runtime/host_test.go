package runtime

import (
	goruntime "runtime"
	"testing"
)

// The status host: the contract's kind, the stamped version only when it has the contract's shape, the build platform.
func TestHostOfStampedVersion(t *testing.T) {
	var c Constants
	c.Host.Kind, c.Host.DevVersion, c.Host.Version = "desktop", "dev", `^(?:dev|[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?)$`
	for version, want := range map[string]string{"0.1.0": "0.1.0", "1.2.3-rc.1": "1.2.3-rc.1", "": "dev", "v0.1.0": "dev", "0.1.0; rm": "dev"} {
		h := hostOf(c, version)
		if h.Kind != "desktop" || h.Version != want || h.OS != goruntime.GOOS || h.Arch != goruntime.GOARCH {
			t.Errorf("hostOf(%q) = %+v, want version %q", version, h, want)
		}
	}
}

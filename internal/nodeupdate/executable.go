package nodeupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"runtime"
)

// Read the executing image, even if an installer has since replaced its pathname.
func ExecutableDigest() (string, int64, error) {
	path, err := os.Executable()
	if err != nil {
		return "", 0, err
	}
	if runtime.GOOS == "linux" {
		path = "/proc/self/exe"
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), size, err
}

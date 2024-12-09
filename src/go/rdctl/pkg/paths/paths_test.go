package paths

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
)

const fakeResourcesPath = "fakePath"

func mockGetResourcesPath() (string, error) {
	return fakeResourcesPath, nil
}

func TestGetResourcesPath(t *testing.T) {
	dir := t.TempDir()
	fileName := filepath.Join(dir, "resources", runtime.GOOS, "bin", "rdctl")
	actual, err := GetResourcesPath(fileName)
	if assert.NoError(t, err) {
		assert.Equal(t, filepath.Join(dir, "resources"), actual)
	}
}

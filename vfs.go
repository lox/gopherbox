package gopherbox

import (
	"path/filepath"

	"github.com/spf13/afero"
)

// InMemoryFs returns a pure in-memory filesystem.
func InMemoryFs() afero.Fs {
	return afero.NewMemMapFs()
}

// OverlayFs returns a copy-on-write filesystem over a real directory.
// Reads come from disk, writes stay in memory.
func OverlayFs(root string) afero.Fs {
	cleanRoot := filepath.Clean(root)
	base := afero.NewBasePathFs(afero.NewOsFs(), cleanRoot)
	return afero.NewCopyOnWriteFs(afero.NewReadOnlyFs(base), afero.NewMemMapFs())
}

// ReadWriteFs returns a jailed real filesystem.
// Reads and writes go to disk, but cannot escape root.
func ReadWriteFs(root string) afero.Fs {
	cleanRoot := filepath.Clean(root)
	return afero.NewBasePathFs(afero.NewOsFs(), cleanRoot)
}

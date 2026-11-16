//go:build !unix

package blob

import "os"

// Platforms without flock rely on one process holding the data folder at
// a time; a partial is then guarded only by the store's own lock.
func lockExclusive(*os.File) error   { return nil }
func unlockExclusive(*os.File) error { return nil }

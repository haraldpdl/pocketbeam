package main

import "os"

// syncClose flushes a file's contents to the storage medium and then
// closes it. Every file that is later renamed into its final name has to
// be closed this way: closing alone only hands the data to the page
// cache, so a power cut can leave the rename recorded while the bytes
// behind it are not. On the device's vfat storage that produces a
// complete-looking but empty or truncated book, or an unrunnable app
// binary after a self-update.
//
// Close runs even when Sync fails so the descriptor never leaks; the
// Sync error wins because it is the one that says the data is not safe.
func syncClose(f *os.File) error {
	err := f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// syncDir flushes a directory so a rename into it survives a power cut:
// fsync on the file covers the file's own data, not the directory entry
// that gives it its final name. Best effort by design - a filesystem
// that refuses fsync on a directory still has the file's contents on the
// medium, which is what keeps a half-written book from masquerading as a
// finished one.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// Package fstest is a drop-in replacement for the standard testing/fstest
// package which adds a few extensions that have proven useful when testing
// implementations of the fs.FS interface.
//
// For a full documentation of the standard testing/fs package see:
// https://pkg.go.dev/testing/fstest
package fstest

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"testing/fstest"
	"time"

	"github.com/stealthrocket/fsinfo"
	"github.com/stealthrocket/fslink"
)

func TestFS(fsys fs.FS, expected ...string) error {
	return fstest.TestFS(fsys, expected...)
}

type MapFile = fstest.MapFile

type MapFS fstest.MapFS

func (fsys MapFS) Glob(pattern string) ([]string, error) {
	return fstest.MapFS(fsys).Glob(pattern)
}

func (fsys MapFS) Open(name string) (fs.File, error) {
	f, err := fstest.MapFS(fsys).Open(name)
	if err != nil {
		return nil, err
	}
	s, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if s.IsDir() && fsys[name] == nil { // virtual directory?
		return virtualDirectory{f.(fs.ReadDirFile)}, nil
	}
	if (s.Mode().Perm() & 0400) == 0 {
		return denyReadPermission{f}, nil
	}
	return f, nil
}

func (fsys MapFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return fstest.MapFS(fsys).ReadDir(name)
}

func (fsys MapFS) ReadFile(name string) ([]byte, error) {
	return fstest.MapFS(fsys).ReadFile(name)
}

func (fsys MapFS) Stat(name string) (fs.FileInfo, error) {
	return fstest.MapFS(fsys).Stat(name)
}

func (fsys MapFS) Sub(name string) (fs.FS, error) {
	_, err := fs.Stat(fsys, name)
	if err != nil {
		return nil, err
	}
	return &subFS{fsys, name}, nil
}

func (fsys MapFS) ReadLink(name string) (string, error) {
	if !fs.ValidPath(name) {
		return "", &fs.PathError{"readlink", name, fs.ErrNotExist}
	}
	file := fsys[name]
	if file == nil {
		return "", &fs.PathError{"readlink", name, fs.ErrNotExist}
	}
	if (file.Mode & fs.ModeSymlink) == 0 {
		return "", &fs.PathError{"readlink", name, fs.ErrInvalid}
	}
	return string(file.Data), nil
}

type subFS struct {
	fsys MapFS
	name string
}

func (f *subFS) fullName(name string) string {
	if name == "." {
		name = f.name
	} else {
		name = f.name + "/" + name
	}
	return name
}

func (f *subFS) Open(name string) (fs.File, error) {
	return f.fsys.Open(f.fullName(name))
}

func (f *subFS) ReadLink(name string) (string, error) {
	return f.fsys.ReadLink(f.fullName(name))
}

var (
	_ fslink.ReadLinkFS = (MapFS)(nil)
)

type denyReadPermission struct{ fs.File }

func (denyReadPermission) Read([]byte) (int, error) { return 0, fs.ErrPermission }

func (denyReadPermission) ReadDir(int) ([]fs.DirEntry, error) { return nil, fs.ErrPermission }

type virtualDirectory struct{ fs.ReadDirFile }

func (d virtualDirectory) Stat() (fs.FileInfo, error) {
	stat, err := d.ReadDirFile.Stat()
	return virtualDirInfo{stat}, err
}

type virtualDirInfo struct{ fs.FileInfo }

func (virtualDirInfo) Mode() fs.FileMode { return fs.ModeDir | 0700 }

const equalFSMinSize = 1024
const equalFSBufSize = 32768

// EqualFS compares two file systems, returning nil if they are equal, or an
// error describing their difference when they are not.
func EqualFS(a, b fs.FS) error { return EqualFSBuffer(a, b, nil) }

// EqualFSBuffer is like EqualFS but the function receives the buffer used to
// read files as arguments.
func EqualFSBuffer(a, b fs.FS, buf []byte) error {
	if len(buf) < equalFSMinSize {
		buf = make([]byte, equalFSBufSize)
	}
	return equalDir(a, b, ".", buf)
}

func equalSymlink(source, target fs.FS, name string) error {
	sourceLink, err := fslink.ReadLink(source, name)
	if err != nil {
		return err
	}
	targetLink, err := fslink.ReadLink(target, name)
	if err != nil {
		return err
	}
	if sourceLink != targetLink {
		return equalErrorf(name, "symbolic links mimatch: want=%q got=%q", sourceLink, targetLink)
	}
	return nil
}

func equalDir(source, target fs.FS, name string, buf []byte) error {
	sourceEntries, err := fs.ReadDir(source, name)
	if err != nil {
		return err
	}
	targetEntries, err := fs.ReadDir(target, name)
	if err != nil {
		return err
	}
	if len(sourceEntries) != len(targetEntries) {
		return equalErrorf(name, "number of directory entries mismatch: want=%d got=%d", len(sourceEntries), len(targetEntries))
	}
	for i := range sourceEntries {
		sourceEntry := sourceEntries[i]
		targetEntry := targetEntries[i]

		sourceName := sourceEntry.Name()
		targetName := targetEntry.Name()
		if sourceName != targetName {
			return equalErrorf(name, "name of directory entry %d mismatch: want=%q got=%q", i, sourceName, targetName)
		}

		sourceType := sourceEntry.Type()
		targetType := targetEntry.Type()
		if sourceType != targetType {
			return equalErrorf(name, "name of directory entry %q mismatch: want=%v got=%v", sourceName, sourceType, targetType)
		}

		var filePath = path.Join(name, sourceName)
		var err error
		switch sourceType {
		case fs.ModeSymlink:
			err = equalSymlink(source, target, filePath)
		case fs.ModeDir:
			err = equalDir(source, target, filePath, buf)
		case 0: // regular
			err = equalFile(source, target, filePath, buf)
		default:
			err = equalNode(source, target, filePath)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func equalFile(source, target fs.FS, name string, buf []byte) error {
	if err := equalStat(source, target, name); err != nil {
		return equalErrorf(name, "%w", err)
	}
	sourceFile, err1 := source.Open(name)
	if err1 == nil {
		defer sourceFile.Close()
	}
	targetFile, err2 := target.Open(name)
	if err2 == nil {
		defer targetFile.Close()
	}
	if err1 != nil || err2 != nil {
		if !errors.Is(err1, unwrap(err2)) {
			return equalErrorf(name, "file open error mismatch: want=%v got=%v", err1, err2)
		}
	}
	if err := equalData(sourceFile, targetFile, buf); err != nil {
		return equalErrorf(name, "%w", err)
	}
	return nil
}

func equalNode(source, target fs.FS, name string) error {
	if err := equalStat(source, target, name); err != nil {
		return equalErrorf(name, "%w", err)
	}
	return nil
}

func equalData(source, target fs.File, buf []byte) error {
	buf1 := buf[:len(buf)/2]
	buf2 := buf[len(buf)/2:]
	for {
		n1, err1 := source.Read(buf1)
		n2, err2 := target.Read(buf2)
		if n1 != n2 {
			return fmt.Errorf("file read size mismatch: want=%d got=%d", n1, n2)
		}
		b1 := buf1[:n1]
		b2 := buf2[:n2]
		if !bytes.Equal(b1, b2) {
			return fmt.Errorf("file content mismatch: want=%q got=%q", b1, b2)
		}
		if err1 != err2 {
			return fmt.Errorf("file read error mismatch: want=%v got=%v", err1, err2)
		}
		if err1 != nil {
			break
		}
	}
	return nil
}

func equalStat(source, target fs.FS, name string) error {
	sourceInfo, err := fs.Stat(source, name)
	if err != nil {
		return err
	}
	targetInfo, err := fs.Stat(target, name)
	if err != nil {
		return err
	}
	sourceMode := sourceInfo.Mode()
	targetMode := targetInfo.Mode()
	sourceType := sourceMode.Type()
	targetType := targetMode.Type()
	if sourceType != targetType {
		return fmt.Errorf("file types mismatch: want=%s got=%s", sourceType, targetType)
	}
	sourcePerm := sourceMode.Perm()
	targetPerm := targetMode.Perm()
	// Sometimes the permission bits may not be available. Clearly we were able
	// to open the files so we should have at least read permissions reported so
	// just ignore the permissions if either the source or target are zero. This
	// happens with virtualized directories for fstest.MapFS for example.
	if sourcePerm != 0 && targetPerm != 0 && sourcePerm != targetPerm {
		return fmt.Errorf("file modes mismatch: want=%s got=%s", sourceMode, targetMode)
	}
	sourceModTime := fsinfo.ModTime(sourceInfo)
	targetModTime := fsinfo.ModTime(targetInfo)
	if err := equalTime("modification", sourceModTime, targetModTime); err != nil {
		return err
	}
	sourceAccessTime := fsinfo.AccessTime(sourceInfo)
	targetAccessTime := fsinfo.AccessTime(targetInfo)
	if err := equalTime("access", sourceAccessTime, targetAccessTime); err != nil {
		return err
	}
	sourceChangeTime := fsinfo.ChangeTime(sourceInfo)
	targetChangeTime := fsinfo.ChangeTime(targetInfo)
	if err := equalTime("change", sourceChangeTime, targetChangeTime); err != nil {
		return err
	}
	// Directory sizes are platform-dependent, there is no need to compare.
	if !sourceInfo.IsDir() {
		sourceSize := sourceInfo.Size()
		targetSize := targetInfo.Size()
		if sourceSize != targetSize {
			return fmt.Errorf("files sizes mismatch: want=%d got=%d", sourceSize, targetSize)
		}
	}
	return nil
}

func equalTime(typ string, source, target time.Time) error {
	// Only compare the modification times if both file systems support it,
	// assuming a zero time means it's not supported.
	if !source.IsZero() && !target.IsZero() && !source.Equal(target) {
		return fmt.Errorf("file %s times mismatch: want=%v got=%v", typ, source, target)
	}
	return nil
}

func equalErrorf(name, msg string, args ...any) error {
	return &fs.PathError{Op: "equal", Path: name, Err: fmt.Errorf(msg, args...)}
}

func unwrap(err error) error {
	for {
		if cause := errors.Unwrap(err); cause == nil {
			return err
		} else {
			err = cause
		}
	}
}

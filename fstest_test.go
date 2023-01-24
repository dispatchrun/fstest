package fstest_test

import (
	"io/fs"
	"testing"

	"github.com/stealthrocket/fstest"
)

func TestEqualFS(t *testing.T) {
	a := fstest.MapFS{
		"dir":         &fstest.MapFile{Mode: 0755 | fs.ModeDir},
		"dir/file":    &fstest.MapFile{Mode: 0644, Data: []byte("Hello World!")},
		"dir/symlink": &fstest.MapFile{Mode: 0666 | fs.ModeSymlink, Data: []byte("../file")},
	}

	b := fstest.MapFS{
		"dir":         &fstest.MapFile{Mode: 0755 | fs.ModeDir},
		"dir/file":    &fstest.MapFile{Mode: 0644, Data: []byte("Hello World!")},
		"dir/symlink": &fstest.MapFile{Mode: 0666 | fs.ModeSymlink /* broken */},
	}

	if err := fstest.EqualFS(a, a); err != nil {
		t.Error(err)
	}
	if err := fstest.EqualFS(a, b); err == nil {
		t.Error(err)
	}
}

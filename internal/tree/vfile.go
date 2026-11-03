package tree

import (
	"bytes"

	billy "github.com/go-git/go-billy/v5"
)

// vfile 是打开后的文件：内容已在打开时快照，Read/ReadAt/Seek 走内存。
type vfile struct {
	name string
	r    *bytes.Reader
}

func (f *vfile) Name() string                              { return f.name }
func (f *vfile) Read(p []byte) (int, error)                { return f.r.Read(p) }
func (f *vfile) ReadAt(p []byte, off int64) (int, error)   { return f.r.ReadAt(p, off) }
func (f *vfile) Seek(off int64, whence int) (int64, error) { return f.r.Seek(off, whence) }
func (f *vfile) Close() error                              { return nil }
func (f *vfile) Write(p []byte) (int, error)               { return 0, errReadOnly }
func (f *vfile) Lock() error                               { return billy.ErrNotSupported }
func (f *vfile) Unlock() error                             { return billy.ErrNotSupported }
func (f *vfile) Truncate(int64) error                      { return errReadOnly }

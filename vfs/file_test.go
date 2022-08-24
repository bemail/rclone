package vfs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	_ "path"
	"strings"
	_ "strings"
	"testing"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/fs/operations"
	"github.com/rclone/rclone/fstest"
	"github.com/rclone/rclone/fstest/mockfs"
	"github.com/rclone/rclone/fstest/mockobject"
	"github.com/rclone/rclone/vfs/vfscommon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fileCreateLinks(t *testing.T, mode vfscommon.CacheMode, links bool) (r *fstest.Run, vfs *VFS, fh *File, item fstest.Item, lh *File, linkItem fstest.Item, cleanup func()) {
	opt := vfscommon.DefaultOpt
	opt.CacheMode = mode
	opt.WriteBack = writeBackDelay
	opt.Links = links
	r, vfs, cleanup = newTestVFSOpt(t, &opt)

	file1 := r.WriteObject(context.Background(), "dir/file1", "file1 contents", t1)
	r.CheckRemoteItems(t, file1)

	node, err := vfs.Stat(file1.Path)
	require.NoError(t, err)
	require.True(t, node.Mode().IsRegular())

	// For some reason this does not works - Stat fails and return file not found ?
	/*
		// Force create a link named link1[.rclonelink] in root pointing to dir/file1
		link1 := r.WriteObject(context.Background(), "dir2/link1"+fs.LinkSuffix, "dir/file1", t1)
		r.CheckRemoteItems(t, file1, link1)

		linkNode, err := vfs.Stat(link1.Path)
		require.NoError(t, err)
		if (links) {
			require.True(t, linkNode.Mode() & os.ModeSymlink != 0)
		} else {
			require.True(t, linkNode.Mode().IsRegular())
		}
	*/

	return r, vfs, node.(*File), file1, nil /*linkNode.(*File)*/, fstest.Item{} /*link1*/, cleanup
}

func fileCreate(t *testing.T, mode vfscommon.CacheMode) (r *fstest.Run, vfs *VFS, fh *File, item fstest.Item, cleanup func()) {
	r, vfs, fh, item, _, _, cleanup = fileCreateLinks(t, mode, false)
	return r, vfs, fh, item, cleanup
}

// newItemFromFile creates a fstest.item from a vfs file
func newItemFromFile(file *File) fstest.Item {
	fh, err := file.Open(os.O_RDONLY)
	if err != nil {
		log.Fatalf("Failed to open file for reading: %v", err)
	}

	content, err := ioutil.ReadAll(fh)
	if err != nil {
		log.Fatalf("Failed to read all file: %v", err)
	}

	err = fh.Close()
	if err != nil {
		log.Fatalf("Failed to close file: %v", err)
	}

	hash := hash.NewMultiHasher()
	buf := bytes.NewBufferString(string(content))
	_, err = io.Copy(hash, buf)
	if err != nil {
		log.Fatalf("Failed to create item: %v", err)
	}

	i := fstest.Item{
		Path:    file.Path(),
		ModTime: file.ModTime(),
		Size:    file.Size(),
		Hashes:  hash.Sums(),
	}
	return i
}

func TestFileMethods(t *testing.T) {
	r, vfs, file, _, cleanup := fileCreate(t, vfscommon.CacheModeOff)
	defer cleanup()

	// String
	assert.Equal(t, "dir/file1", file.String())
	assert.Equal(t, "<nil *File>", (*File)(nil).String())

	// IsDir
	assert.Equal(t, false, file.IsDir())

	// IsFile
	assert.Equal(t, true, file.IsFile())

	// Mode
	assert.Equal(t, vfs.Opt.FilePerms, file.Mode())

	// Name
	assert.Equal(t, "file1", file.Name())

	// Path
	assert.Equal(t, "dir/file1", file.Path())

	// Sys
	assert.Equal(t, nil, file.Sys())

	// SetSys
	file.SetSys(42)
	assert.Equal(t, 42, file.Sys())

	// Inode
	assert.NotEqual(t, uint64(0), file.Inode())

	// Node
	assert.Equal(t, file, file.Node())

	// ModTime
	assert.WithinDuration(t, t1, file.ModTime(), r.Fremote.Precision())

	// Size
	assert.Equal(t, int64(14), file.Size())

	// Sync
	assert.NoError(t, file.Sync())

	// DirEntry
	assert.Equal(t, file.o, file.DirEntry())

	// Dir
	assert.Equal(t, file.d, file.Dir())

	// VFS
	assert.Equal(t, vfs, file.VFS())
}

func testFileSetModTime(t *testing.T, cacheMode vfscommon.CacheMode, open bool, write bool) {
	if !canSetModTimeValue {
		t.Skip("can't set mod time")
	}
	r, vfs, file, file1, cleanup := fileCreate(t, cacheMode)
	defer cleanup()
	if !canSetModTime(t, r) {
		t.Skip("can't set mod time")
	}

	var (
		err      error
		fd       Handle
		contents = "file1 contents"
	)
	if open {
		// Open with write intent
		if cacheMode != vfscommon.CacheModeOff {
			fd, err = file.Open(os.O_WRONLY)
			if write {
				contents = "hello contents"
			}
		} else {
			// Can't write without O_TRUNC with CacheMode Off
			fd, err = file.Open(os.O_WRONLY | os.O_TRUNC)
			if write {
				contents = "hello"
			} else {
				contents = ""
			}
		}
		require.NoError(t, err)

		// Write some data
		if write {
			_, err = fd.WriteString("hello")
			require.NoError(t, err)
		}
	}

	err = file.SetModTime(t2)
	require.NoError(t, err)

	if open {
		require.NoError(t, fd.Close())
		vfs.WaitForWriters(waitForWritersDelay)
	}

	file1 = fstest.NewItem(file1.Path, contents, t2)
	r.CheckRemoteItems(t, file1)

	vfs.Opt.ReadOnly = true
	err = file.SetModTime(t2)
	assert.Equal(t, EROFS, err)
}

// Test various combinations of setting mod times with and
// without the cache and with and without opening or writing
// to the file.
//
// Each of these tests a different path through the VFS code.
func TestFileSetModTime(t *testing.T) {
	for _, cacheMode := range []vfscommon.CacheMode{vfscommon.CacheModeOff, vfscommon.CacheModeFull} {
		for _, open := range []bool{false, true} {
			for _, write := range []bool{false, true} {
				if write && !open {
					continue
				}
				t.Run(fmt.Sprintf("cache=%v,open=%v,write=%v", cacheMode, open, write), func(t *testing.T) {
					testFileSetModTime(t, cacheMode, open, write)
				})
			}
		}
	}
}

func fileCheckGivenContents(t *testing.T, file *File, expected string) {
	fd, err := file.Open(os.O_RDONLY)
	require.NoError(t, err)

	contents, err := ioutil.ReadAll(fd)
	require.NoError(t, err)
	assert.Equal(t, expected, string(contents))

	require.NoError(t, fd.Close())
}

func fileCheckContents(t *testing.T, file *File) {
	fileCheckGivenContents(t, file, "file1 contents")
}

func TestFileOpenRead(t *testing.T) {
	_, _, file, _, cleanup := fileCreate(t, vfscommon.CacheModeOff)
	defer cleanup()

	fileCheckContents(t, file)
}

func TestFileOpenReadUnknownSize(t *testing.T) {
	var (
		contents = []byte("file contents")
		remote   = "file.txt"
		ctx      = context.Background()
	)

	// create a mock object which returns size -1
	o := mockobject.New(remote).WithContent(contents, mockobject.SeekModeNone)
	o.SetUnknownSize(true)
	assert.Equal(t, int64(-1), o.Size())

	// add it to a mock fs
	f := mockfs.NewFs(context.Background(), "test", "root")
	f.AddObject(o)
	testObj, err := f.NewObject(ctx, remote)
	require.NoError(t, err)
	assert.Equal(t, int64(-1), testObj.Size())

	// create a VFS from that mockfs
	vfs := New(f, nil)
	defer cleanupVFS(t, vfs)

	// find the file
	node, err := vfs.Stat(remote)
	require.NoError(t, err)
	require.True(t, node.IsFile())
	file := node.(*File)

	// open it
	fd, err := file.openRead()
	require.NoError(t, err)
	assert.Equal(t, int64(0), fd.Size())

	// check the contents are not empty even though size is empty
	gotContents, err := ioutil.ReadAll(fd)
	require.NoError(t, err)
	assert.Equal(t, contents, gotContents)
	t.Logf("gotContents = %q", gotContents)

	// check that file size has been updated
	assert.Equal(t, int64(len(contents)), fd.Size())

	require.NoError(t, fd.Close())
}

func TestFileOpenWrite(t *testing.T) {
	_, vfs, file, _, cleanup := fileCreate(t, vfscommon.CacheModeOff)
	defer cleanup()

	fd, err := file.openWrite(os.O_WRONLY | os.O_TRUNC)
	require.NoError(t, err)

	newContents := []byte("this is some new contents")
	n, err := fd.Write(newContents)
	require.NoError(t, err)
	assert.Equal(t, len(newContents), n)
	require.NoError(t, fd.Close())

	assert.Equal(t, int64(25), file.Size())

	vfs.Opt.ReadOnly = true
	_, err = file.openWrite(os.O_WRONLY | os.O_TRUNC)
	assert.Equal(t, EROFS, err)
}

func TestFileRemove(t *testing.T) {
	r, vfs, file, _, cleanup := fileCreate(t, vfscommon.CacheModeOff)
	defer cleanup()

	err := file.Remove()
	require.NoError(t, err)

	r.CheckRemoteItems(t)

	vfs.Opt.ReadOnly = true
	err = file.Remove()
	assert.Equal(t, EROFS, err)
}

func TestFileRemoveAll(t *testing.T) {
	r, vfs, file, _, cleanup := fileCreate(t, vfscommon.CacheModeOff)
	defer cleanup()

	err := file.RemoveAll()
	require.NoError(t, err)

	r.CheckRemoteItems(t)

	vfs.Opt.ReadOnly = true
	err = file.RemoveAll()
	assert.Equal(t, EROFS, err)
}

func TestFileOpen(t *testing.T) {
	_, _, file, _, cleanup := fileCreate(t, vfscommon.CacheModeOff)
	defer cleanup()

	fd, err := file.Open(os.O_RDONLY)
	require.NoError(t, err)
	_, ok := fd.(*ReadFileHandle)
	assert.True(t, ok)
	require.NoError(t, fd.Close())

	fd, err = file.Open(os.O_WRONLY)
	assert.NoError(t, err)
	_, ok = fd.(*WriteFileHandle)
	assert.True(t, ok)
	require.NoError(t, fd.Close())

	fd, err = file.Open(os.O_RDWR)
	assert.NoError(t, err)
	_, ok = fd.(*WriteFileHandle)
	assert.True(t, ok)
	require.NoError(t, fd.Close())

	_, err = file.Open(3)
	assert.Equal(t, EPERM, err)
}

func testFileRename(t *testing.T, mode vfscommon.CacheMode, inCache bool, forceCache bool) {
	r, vfs, file, item, cleanup := fileCreate(t, mode)
	defer cleanup()

	if !operations.CanServerSideMove(r.Fremote) {
		t.Skip("skip as can't rename files")
	}

	rootDir, err := vfs.Root()
	require.NoError(t, err)

	// force the file into the cache if required
	if forceCache {
		// write the file with read and write
		fd, err := file.Open(os.O_RDWR | os.O_CREATE | os.O_TRUNC)
		require.NoError(t, err)

		n, err := fd.Write([]byte("file1 contents"))
		require.NoError(t, err)
		require.Equal(t, 14, n)

		require.NoError(t, file.SetModTime(item.ModTime))

		err = fd.Close()
		require.NoError(t, err)
	}
	vfs.WaitForWriters(waitForWritersDelay)

	// check file in cache
	if inCache {
		// read contents to get file in cache
		fileCheckContents(t, file)
		assert.True(t, vfs.cache.Exists(item.Path))
	}

	dir := file.Dir()

	// start with "dir/file1"
	r.CheckRemoteItems(t, item)

	// rename file to "newLeaf"
	err = dir.Rename("file1", "newLeaf", rootDir)
	require.NoError(t, err)

	item.Path = "newLeaf"
	r.CheckRemoteItems(t, item)

	// check file in cache
	if inCache {
		assert.True(t, vfs.cache.Exists(item.Path))
	}

	// check file exists in the vfs layer at its new name
	_, err = vfs.Stat("newLeaf")
	require.NoError(t, err)

	// rename it back to "dir/file1"
	err = rootDir.Rename("newLeaf", "file1", dir)
	require.NoError(t, err)

	item.Path = "dir/file1"
	r.CheckRemoteItems(t, item)

	// check file in cache
	if inCache {
		assert.True(t, vfs.cache.Exists(item.Path))
	}

	// now try renaming it with the file open
	// first open it and write to it but don't close it
	fd, err := file.Open(os.O_WRONLY | os.O_TRUNC)
	require.NoError(t, err)
	newContents := []byte("this is some new contents")
	_, err = fd.Write(newContents)
	require.NoError(t, err)

	// rename file to "newLeaf"
	err = dir.Rename("file1", "newLeaf", rootDir)
	require.NoError(t, err)
	newItem := fstest.NewItem("newLeaf", string(newContents), item.ModTime)

	// check file has been renamed immediately in the cache
	if inCache {
		assert.True(t, vfs.cache.Exists("newLeaf"))
	}

	// check file exists in the vfs layer at its new name
	_, err = vfs.Stat("newLeaf")
	require.NoError(t, err)

	// Close the file
	require.NoError(t, fd.Close())

	// Check file has now been renamed on the remote
	item.Path = "newLeaf"
	vfs.WaitForWriters(waitForWritersDelay)
	fstest.CheckListingWithPrecision(t, r.Fremote, []fstest.Item{newItem}, nil, fs.ModTimeNotSupported)
}

func TestFileRename(t *testing.T) {
	for _, test := range []struct {
		mode       vfscommon.CacheMode
		inCache    bool
		forceCache bool
	}{
		{mode: vfscommon.CacheModeOff, inCache: false},
		{mode: vfscommon.CacheModeMinimal, inCache: false},
		{mode: vfscommon.CacheModeMinimal, inCache: true, forceCache: true},
		{mode: vfscommon.CacheModeWrites, inCache: false},
		{mode: vfscommon.CacheModeWrites, inCache: true, forceCache: true},
		{mode: vfscommon.CacheModeFull, inCache: true},
	} {
		t.Run(fmt.Sprintf("%v,forceCache=%v", test.mode, test.forceCache), func(t *testing.T) {
			testFileRename(t, test.mode, test.inCache, test.forceCache)
		})
	}
}

func renameSymlinkAndCheck(t *testing.T, r *fstest.Run, vfs *VFS, fileItem fstest.Item, inCache bool, linkSourceFilePath, linkTargetFilePath string) {
	sourceFilePath, _ /*sourceIsLink*/ := vfs.TrimSymlink(linkSourceFilePath)
	targetFilePath, _ /*targetIsLink*/ := vfs.TrimSymlink(linkTargetFilePath)

	// Rename source to target
	err := vfs.Rename(sourceFilePath, targetFilePath)
	require.NoError(t, err)

	// check source link has been renamed immediately in the cache
	if inCache {
		assert.False(t, vfs.cache.Exists(linkSourceFilePath))

		if vfs.Opt.CacheMode >= vfscommon.CacheModeWrites {
			assert.True(t, vfs.cache.Exists(linkTargetFilePath))
		}
	}

	// check source link does not exists anymore in the vfs layer
	_, err = vfs.Stat(sourceFilePath)
	require.Error(t, err)

	// check link exists in the vfs layer at its new name
	link, err := vfs.Stat(targetFilePath)
	require.NoError(t, err)

	// Check link path match new path
	require.Equal(t, linkTargetFilePath, link.Path())

	// Check link has now been renamed on the remote
	vfs.WaitForWriters(waitForWritersDelay)
	fstest.CheckListingWithPrecision(t, r.Fremote, []fstest.Item{fileItem, newItemFromFile(link.(*File))}, nil, fs.ModTimeNotSupported)

	// Check link contents
	linkData, err := vfs.Readlink(targetFilePath)
	require.NoError(t, err)
	require.Equal(t, linkData, "dir/file1")
}

func testFileSymlinks(t *testing.T, mode vfscommon.CacheMode, inCache bool, forceCache bool, links bool) {
	r, vfs, file, fileItem, _ /*link*/, _ /*linkItem*/, cleanup := fileCreateLinks(t, mode, links)
	defer cleanup()

	rootDir, err := vfs.Root()
	require.NoError(t, err)

	// force the file into the cache if required
	if forceCache {
		// write the file with read and write
		fd, err := file.Open(os.O_RDWR | os.O_CREATE | os.O_TRUNC)
		require.NoError(t, err)

		n, err := fd.Write([]byte("file1 contents"))
		require.NoError(t, err)
		require.Equal(t, 14, n)

		require.NoError(t, file.SetModTime(fileItem.ModTime))

		err = fd.Close()
		require.NoError(t, err)
	}
	vfs.WaitForWriters(waitForWritersDelay)

	// check file in cache
	if inCache {
		// read contents to get file in cache
		fileCheckContents(t, file)
		assert.True(t, vfs.cache.Exists(fileItem.Path))
	}

	// start with "dir/file1"
	r.CheckRemoteItems(t, fileItem)

	// check file exists in the vfs layer at its new name
	_, err = vfs.Stat("dir/file1")
	require.NoError(t, err)

	// Check file has now been on the remote
	vfs.WaitForWriters(waitForWritersDelay)
	fstest.CheckListingWithPrecision(t, r.Fremote, []fstest.Item{fileItem}, nil, fs.ModTimeNotSupported)

	if !operations.CanServerSideMove(r.Fremote) {
		t.Skip("skip as can't rename files")
	}

	// Set link name
	fullLinkName := "link1" + fs.LinkSuffix
	linkName, isLink := vfs.TrimSymlink(fullLinkName)

	// Check link name
	if links {
		assert.True(t, linkName == strings.TrimSuffix(fullLinkName, fs.LinkSuffix))
	} else {
		assert.True(t, linkName == fullLinkName)
	}

	// Check link type
	assert.True(t, isLink == links)

	// Create symlink
	err = vfs.Symlink("dir/file1", linkName)
	require.NoError(t, err)

	// Stat created symlink
	link, err := vfs.Stat(linkName)
	require.NoError(t, err)

	// force the link into the cache if required
	if forceCache {
		// write the file with read and write
		fd, err := link.Open(os.O_RDWR | os.O_CREATE | os.O_TRUNC)
		require.NoError(t, err)

		n, err := fd.Write([]byte("dir/file1"))
		require.NoError(t, err)
		require.Equal(t, 9, n)

		err = fd.Close()
		require.NoError(t, err)
	}
	vfs.WaitForWriters(waitForWritersDelay)

	// check link in cache
	if inCache {
		// read contents to get link in cache
		fileCheckGivenContents(t, link.(*File), "dir/file1")
		assert.True(t, vfs.cache.Exists(link.Path()))
	}

	// Check link contents
	linkData, err := vfs.Readlink(linkName)
	require.NoError(t, err)
	assert.Equal(t, linkData, "dir/file1")

	// Check link in remote
	linkItem := newItemFromFile(link.(*File))
	r.CheckRemoteItems(t, fileItem, linkItem)

	renameSymlinkAndCheck(t, r, vfs, fileItem, inCache, "link1"+fs.LinkSuffix, "link2"+fs.LinkSuffix)
	renameSymlinkAndCheck(t, r, vfs, fileItem, inCache, "link2"+fs.LinkSuffix, "dir/link3"+fs.LinkSuffix)
	renameSymlinkAndCheck(t, r, vfs, fileItem, inCache, "dir/link3"+fs.LinkSuffix, "link1"+fs.LinkSuffix)

	// Remove link
	err = rootDir.RemoveName(linkName)
	require.NoError(t, err)
	/*link, err = vfs.Stat(linkName)
	require.NoError(t, err)
	require.NoError(t, link.Remove())*/

	// check link has been removed immediately from the cache
	if inCache {
		assert.False(t, vfs.cache.Exists(fullLinkName))
	}

	// Check link removed from remote
	r.CheckRemoteItems(t, fileItem)
}

func TestFileSymlinks(t *testing.T) {
	for _, test := range []struct {
		mode       vfscommon.CacheMode
		inCache    bool
		forceCache bool
		links      bool
	}{
		{mode: vfscommon.CacheModeOff, inCache: false},
		{mode: vfscommon.CacheModeOff, inCache: false, links: true},
		{mode: vfscommon.CacheModeMinimal, inCache: false},
		{mode: vfscommon.CacheModeMinimal, inCache: false, links: true},
		{mode: vfscommon.CacheModeMinimal, inCache: true, forceCache: true},
		{mode: vfscommon.CacheModeMinimal, inCache: true, forceCache: true, links: true},
		{mode: vfscommon.CacheModeWrites, inCache: false},
		{mode: vfscommon.CacheModeWrites, inCache: false, links: true},
		{mode: vfscommon.CacheModeWrites, inCache: true, forceCache: true},
		{mode: vfscommon.CacheModeWrites, inCache: true, forceCache: true, links: true},
		{mode: vfscommon.CacheModeFull, inCache: true},
		{mode: vfscommon.CacheModeFull, inCache: true, links: true},
	} {
		t.Run(fmt.Sprintf("%v,forceCache=%v,links=%v", test.mode, test.forceCache, test.links), func(t *testing.T) {
			testFileSymlinks(t, test.mode, test.inCache, test.forceCache, test.links)
		})
	}
}

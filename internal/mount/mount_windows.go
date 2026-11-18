//go:build windows

package mount

import "errors"

// Mount windows 暂缓：内置 NFS 客户端走 WNetAddConnection2，待接入。
func Mount(serverAddr, mountpoint string) error {
	return errors.New("mount: windows not supported yet")
}

// Unmount windows 暂缓。
func Unmount(mountpoint string) error {
	return errors.New("mount: windows not supported yet")
}

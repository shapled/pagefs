//go:build darwin

package mount

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"

	"golang.org/x/sys/unix"
)

// Mount 走 /sbin/mount_nfs helper：macOS 的 NFS 挂载没有库 API，
// 系统自身的挂载流程也 exec 它。
func Mount(serverAddr, mountpoint string) error {
	if _, err := os.Stat(mountpoint); err != nil {
		return fmt.Errorf("mountpoint %s: %w", mountpoint, err)
	}
	host, port, err := net.SplitHostPort(serverAddr)
	if err != nil {
		host, port = serverAddr, ""
	}
	// macOS mount_nfs 不认 host:port 形式的目标，端口只能走 -o port/mountport。
	// timeout 单位是十分之一秒：默认 0.8s，而读路径会同步触发 list 事件
	// （CDP 调用秒级抖动），上调到 30s 防 "not responding" 弹窗。
	opts := "vers=3,tcp,nolocks,noacl,actimeo=0,noresvport,timeout=300"
	if port != "" {
		if _, err := strconv.Atoi(port); err == nil {
			opts = "port=" + port + ",mountport=" + port + "," + opts
		}
	}
	out, err := exec.Command("/sbin/mount_nfs", "-o", opts, host+":/", mountpoint).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mount_nfs: %v: %s", err, out)
	}
	return nil
}

// Unmount 优先 unmount(2)，失败兜底 exec /sbin/umount。
func Unmount(mountpoint string) error {
	if err := unix.Unmount(mountpoint, 0); err != nil {
		if cmdErr := exec.Command("/sbin/umount", mountpoint).Run(); cmdErr != nil {
			return fmt.Errorf("unmount %s: %v (unmount(2): %v)", mountpoint, cmdErr, err)
		}
	}
	return nil
}

//go:build linux

package mount

import (
	"fmt"
	"net"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

// Mount 优先 mount(2) syscall 直挂，失败兜底 exec mount.nfs
// （帮手程序做选项协商更健壮）。
func Mount(serverAddr, mountpoint string) error {
	if _, err := os.Stat(mountpoint); err != nil {
		return fmt.Errorf("mountpoint %s: %w", mountpoint, err)
	}
	target := net.JoinHostPort(hostOf(serverAddr), portOf(serverAddr))
	data := "vers=3,tcp,nolocks"
	if p := portOf(serverAddr); p != "" {
		data += ",port=" + p + ",mountport=" + p
	}
	err := unix.Mount(target, mountpoint, "nfs", 0, data)
	if err == nil {
		return nil
	}
	out, cmdErr := exec.Command("mount.nfs", "-o", data, target+":", mountpoint).CombinedOutput()
	if cmdErr != nil {
		return fmt.Errorf("mount: %v (mount.nfs: %v: %s)", err, cmdErr, out)
	}
	return nil
}

// Unmount 走 umount(2)。
func Unmount(mountpoint string) error {
	return unix.Unmount(mountpoint, 0)
}

func hostOf(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

func portOf(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return port
}

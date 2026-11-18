//go:build !windows

package main

import (
	"log"
	"os"
	"syscall"
)

// ensureRoot 保证以 root 运行（挂载需要）；非 root 时以 sudo 重启自身。
func ensureRoot() {
	if os.Geteuid() == 0 {
		return
	}
	log.Println("挂载需要 root：以 sudo 重新执行 ...")
	self, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	if err := syscall.Exec("/usr/bin/sudo", append([]string{"sudo", self}, os.Args[1:]...), os.Environ()); err != nil {
		log.Fatalf("sudo 执行失败: %v", err)
	}
}

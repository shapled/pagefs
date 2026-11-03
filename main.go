// pagefs 把浏览器数据（CDP）映射为一个只读的本地 NFSv3 文件系统，
// 供 agents 与脚本通过 ls / cat 访问。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	nfs "github.com/willscott/go-nfs"
	nfshelper "github.com/willscott/go-nfs/helpers"

	"github.com/shapled/pagefs/internal/helper"
	"github.com/shapled/pagefs/internal/provider/cdp"
	"github.com/shapled/pagefs/internal/tree"
)

func main() {
	cdpAddr := flag.String("cdp", "http://127.0.0.1:9222", "CDP http endpoint")
	listen := flag.String("listen", "127.0.0.1:11111", "NFS server listen address")
	flag.Parse()

	client := cdp.NewClient(*cdpAddr)
	defer client.Close()

	// 启动时先探一次 CDP，尽早暴露配置问题。
	tgts, err := client.Targets(mustCtx())
	if err != nil {
		log.Fatalf("无法连接 CDP %s: %v（请确认浏览器带 --remote-debugging-port 启动）", *cdpAddr, err)
	}
	log.Printf("CDP 连接正常，发现 %d 个页面", len(tgts))

	fsTree := tree.New(helper.NewIDGen(), cdp.NewProvider(client))

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("监听 %s 失败: %v", *listen, err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	handler := nfshelper.NewNullAuthHandler(fsTree)
	cacheHelper := nfshelper.NewCachingHandler(handler, 1024)

	fmt.Printf("pagefs NFSv3 server on %s\n\n挂载（需要 root）:\n"+
		"  sudo mount_nfs -o port=%d,mountport=%d,vers=3,tcp,nolocks,noacl,actimeo=0,noresvport localhost:/ /tmp/pagefs\n\n"+
		"卸载:\n  sudo umount /tmp/pagefs\n", ln.Addr(), port, port)

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		fmt.Println("\n退出。若已挂载请先: sudo umount /tmp/pagefs")
		client.Close()
		os.Exit(0)
	}()

	if err := nfs.Serve(ln, cacheHelper); err != nil {
		log.Fatalf("nfs serve: %v", err)
	}
}

func mustCtx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	go func() { <-time.After(6 * time.Second); cancel() }()
	return ctx
}

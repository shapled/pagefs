// pagefs 把浏览器数据（CDP）映射为本地 NFSv3 文件系统，供 agents 与
// 脚本通过 ls / cat / echo / grep 访问。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	nfs "github.com/willscott/go-nfs"
	nfshelper "github.com/willscott/go-nfs/helpers"

	"github.com/shapled/pagefs/internal/config"
	"github.com/shapled/pagefs/internal/helper"
	"github.com/shapled/pagefs/internal/mount"
	"github.com/shapled/pagefs/internal/provider/cdp"
	"github.com/shapled/pagefs/internal/tree"
)

const usageText = `pagefs —— 把浏览器映射为本地 NFSv3 文件系统。

用法：
  pagefs serve [address]       启动 NFS 服务（默认 127.0.0.1:0，实际地址打印并写入 ~/.pagefs/serve.json）
  pagefs mount <mountpoint>    挂载 serve 中的服务（需要 root）
  pagefs umount <mountpoint>   卸载（需要 root）
  pagefs run [address] <mp>    启动服务并挂载，退出时自动卸载
  pagefs exec <node> [args]    对挂载点内的命令节点执行三件套（写→读→打印）

配置：工作目录下 pagefs.json / pagefs.yml（json 优先）：
  { "listen": "127.0.0.1:0", "cdp": "http://127.0.0.1:9222" }
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usageText)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		cmdServe(os.Args[2:])
	case "mount":
		cmdMount(os.Args[2:])
	case "umount":
		cmdUmount(os.Args[2:])
	case "run":
		cmdRun(os.Args[2:])
	case "exec":
		cmdExec(os.Args[2:])
	case "help", "-h", "--help":
		fmt.Print(usageText)
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 %q\n\n%s", os.Args[1], usageText)
		os.Exit(2)
	}
}

// resolveCDP：flag > 配置文件 > 默认值。
func resolveCDP(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	cfg := mustConfig()
	if cfg.CDP != "" {
		return cfg.CDP
	}
	return "http://127.0.0.1:9222"
}

// resolveListen：位置参数 > 配置文件 > ephemeral。
func resolveListen(positional string) string {
	if positional != "" {
		return positional
	}
	cfg := mustConfig()
	if cfg.Listen != "" {
		return cfg.Listen
	}
	return "127.0.0.1:0"
}

func mustConfig() config.Config {
	cfg, _, err := config.Load(".")
	if err != nil {
		log.Fatalf("读取配置失败: %v", err)
	}
	return cfg
}

// buildTree 探测 CDP 并建树。
func buildTree(cdpAddr string) *tree.Tree {
	client := cdp.NewClient(cdpAddr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	tgts, err := client.Targets(ctx)
	cancel()
	if err != nil {
		log.Fatalf("无法连接 CDP %s: %v（请确认浏览器带 --remote-debugging-port 启动）", cdpAddr, err)
	}
	log.Printf("CDP 连接正常，发现 %d 个页面", len(tgts))
	return tree.New(helper.NewIDGen(), cdp.NewProvider(client))
}

// serveNFS 在 listener 上启动 NFS 服务（goroutine 内阻塞）。
func serveNFS(fsTree *tree.Tree, ln net.Listener) {
	handler := nfshelper.NewNullAuthHandler(fsTree)
	cacheHelper := nfshelper.NewCachingHandler(handler, 1024)
	go func() {
		if err := nfs.Serve(ln, cacheHelper); err != nil {
			log.Fatalf("nfs serve: %v", err)
		}
	}()
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cdpFlag := fs.String("cdp", "", "CDP http endpoint（默认取配置或 http://127.0.0.1:9222）")
	if err := fs.Parse(args); err != nil {
		return
	}
	cdpAddr := resolveCDP(*cdpFlag)
	listenAddr := resolveListen(fs.Arg(0))
	fsTree := buildTree(cdpAddr)

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("监听 %s 失败: %v", listenAddr, err)
	}
	serveNFS(fsTree, ln)
	if err := mount.WriteState(mount.State{Address: ln.Addr().String(), PID: os.Getpid()}); err != nil {
		log.Printf("写入服务状态失败: %v", err)
	}

	port := ln.Addr().(*net.TCPAddr).Port
	fmt.Printf("pagefs NFSv3 服务已就绪：%s（端口 %d）\n挂载: pagefs mount <mountpoint>\n", ln.Addr(), port)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	mount.RemoveState()
	fmt.Println("\n服务退出。")
}

func cmdMount(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "用法: pagefs mount <mountpoint>")
		os.Exit(2)
	}
	ensureRoot()
	st, ok, err := mount.ReadState()
	if err != nil {
		log.Fatal(err)
	}
	if !ok || st.Address == "" {
		log.Fatal("没有运行中的 pagefs 服务（先执行 pagefs serve）")
	}
	if err := os.MkdirAll(args[0], 0o755); err != nil {
		log.Fatalf("创建挂载点失败: %v", err)
	}
	if err := mount.Mount(st.Address, args[0]); err != nil {
		log.Fatalf("挂载失败: %v", err)
	}
	fmt.Printf("已挂载 %s → %s\n", st.Address, args[0])
}

func cmdUmount(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "用法: pagefs umount <mountpoint>")
		os.Exit(2)
	}
	ensureRoot()
	if err := mount.Unmount(args[0]); err != nil {
		log.Fatalf("卸载失败: %v", err)
	}
	fmt.Printf("已卸载 %s\n", args[0])
}

func cmdRun(args []string) {
	ensureRoot()
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cdpFlag := fs.String("cdp", "", "CDP http endpoint（默认取配置或 http://127.0.0.1:9222）")
	if err := fs.Parse(args); err != nil {
		return
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		fmt.Fprintln(os.Stderr, "用法: pagefs run [address] <mountpoint>")
		os.Exit(2)
	}
	mp := fs.Arg(0)
	listenPos := ""
	if fs.NArg() == 2 {
		address := fs.Arg(0)
		mp = fs.Arg(1)
		listenPos = address
	}
	cdpAddr := resolveCDP(*cdpFlag)
	listenAddr := resolveListen(listenPos)
	fsTree := buildTree(cdpAddr)

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("监听 %s 失败: %v", listenAddr, err)
	}
	serveNFS(fsTree, ln)
	if err := os.MkdirAll(mp, 0o755); err != nil {
		log.Fatalf("创建挂载点失败: %v", err)
	}
	if err := mount.Mount(ln.Addr().String(), mp); err != nil {
		log.Fatalf("挂载失败: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	fmt.Printf("pagefs 已挂载 %s → %s（端口 %d），Ctrl-C 退出并自动卸载\n", ln.Addr(), mp, port)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	if err := mount.Unmount(mp); err != nil {
		log.Printf("自动卸载失败（可手动 umount %s）: %v", mp, err)
		return
	}
	fmt.Println("\n已退出并卸载。")
}

// cmdExec 对命令节点执行三件套：写参数 → 读回响应 → 打印。
func cmdExec(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: pagefs exec <node> [args]")
		os.Exit(2)
	}
	node := args[0]
	input := strings.Join(args[1:], " ") + "\n" // 空参数 = 单换行，仍触发（无参动词）

	f, err := os.OpenFile(node, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		log.Fatalf("打开 %s 失败: %v", node, err)
	}
	if _, err := f.WriteString(input); err != nil {
		log.Fatalf("写入失败: %v", err)
	}
	f.Close()

	// 读打开触发命令冲刷，读到的即执行后响应
	out, err := os.ReadFile(node)
	if err != nil {
		log.Fatalf("读取失败: %v", err)
	}
	os.Stdout.Write(out)
}

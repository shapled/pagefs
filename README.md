# pagefs

把浏览器数据映射为本地文件系统，供 AI agents 与脚本通过普通的 `ls / cat / echo / grep` 访问。体验对标 procfs/ftrace：**读 = 查询，写 = 命令**。

通过 [CDP](https://chromedevtools.github.io/devtools-protocol/)（Chrome DevTools Protocol）驱动 Chrome，挂载为 NFSv3（纯用户态，无需任何内核组件）。

```
$ cat /tmp/pagefs/list
tab-elated-impala-1
Google 搜索
https://www.google.com/search?q=...
---
tab-amber-jackal-2
GitHub
https://github.com/...

$ cat /tmp/pagefs/tab-amber-jackal-2/title
GitHub

$ echo "https://example.com" > /tmp/pagefs/open
$ cat /tmp/pagefs/open
ok
https://example.com
/tab-tab-silver-otter-7
```

## 前提

- Chrome 以调试端口启动（或对已运行的 Chrome 配置远程调试）：

  ```
  /Applications/Google Chrome.app/Contents/MacOS/Google Chrome --remote-debugging-port=9222
  ```

- Go 1.25+；macOS / Linux（windows 挂载后置）。

## 命令行

```
pagefs serve [address]        启动 NFS 服务（默认 127.0.0.1:0，地址写入 ~/.pagefs/serve.json）
pagefs mount <mountpoint>     挂载 serve 中的服务（需要 root，自动以 sudo 重启）
pagefs umount <mountpoint>    卸载
pagefs run [address] <mp>     启动服务并挂载，Ctrl-C 退出时自动卸载
pagefs exec <node> [args]     对命令节点执行三件套（写→读→打印）
```

配置文件：工作目录下 `pagefs.json` / `pagefs.yml`（json 优先，并存只读 json）：

```json
{ "listen": "127.0.0.1:0", "cdp": "http://127.0.0.1:9222" }
```

手动挂载（macOS，`pagefs serve` 后查看其打印的端口）：

```
sudo mkdir -p /tmp/pagefs
sudo mount_nfs -o port=<端口>,mountport=<端口>,vers=3,tcp,nolocks,noacl,actimeo=0,noresvport,timeout=300 \
     localhost:/ /tmp/pagefs
```

`actimeo=0` 是必须的：关闭客户端属性缓存，否则会读到陈旧的目录列表和文件大小。

## 文件说明

路径规则：**资源/成员**——动词与名词混排；实体 id 固定 `tab-` 前缀，
`ls / | grep tab-` 即纯 tab 枚举。

```
/help                          本文件系统的自述（目录结构 + 用法）
/list                          索引：三行一组（id / title / url），组间 ---
/open                          写 URL 开新 tab；响应第三行输出新 tab 路径
/status                        只读状态快照（JSON：启动时间 / tab 计数 / 最近错误）
/vars/<param>                  运行时参数：srcTimeout / metaTimeout / cmdFlushDelay（可写热更）
/tab-<id>/title                标题
/tab-<id>/url                  URL
/tab-<id>/contentType          页面 MIME 类型
/tab-<id>/content              页面内容（HTML 取 outerHTML，其余取渲染文本）
/tab-<id>/screenshot.png       当前视口截图（PNG，读时现截）
/tab-<id>/navigate             写 URL 原地跳转（触发即回，后台加载）
/tab-<id>/reload               重载（触发即回，后台加载）
/tab-<id>/back|forward         历史导航（触发即回）
/tab-<id>/activate             置前台（会抢占桌面焦点）
/tab-<id>/close                关闭 tab；`rmdir /tab-<id>` 等价
/tab-<id>/view-source          以源码视图打开本页当前 URL（响应两行：ok|err / 新 tab 路径）
/tab-<id>/evaljs               写 JS 表达式 → ok <JSON结果> / err
```

`<id>` 是 pagefs 生成的词组 id（形如 `tab-swift-otter-42`），分配后稳定不变，
与浏览器内部 id 的映射随 pagefs 进程存活；重启 pagefs 会重新分配。

## 行为说明

- **读 = 查询**：`content`、`screenshot.png`、元数据文件读取时现取，
  返回当下最新的快照；`title`/`url` 实时优先，源不可达时回退最近缓存。
- **写 = 命令**：命令文件写入即触发（含空内容，无参动词靠它）；写入
  `clear` 或 `clr` 是重置：还原为用法说明。响应写入同一文件：第一行
  `ok|err`，其后按命令回显请求原文（URL/表达式）并附输出或错误信息，
  末尾换行；触发词类命令（reload/close/view-source 等）不回显请求。
  命令触发即回（ok = 已受理），页面加载在后台进行，就绪与否由调用方
  自行查询（如 `evaljs` 写 `document.readyState`）。唯一的等待是
  evaljs 的 Promise 落定，超时回 err。
- **rm 特化**：`rmdir /tab-<id>` 等价于 close；`rm -r` 成员报 EROFS 但
  最终 rmdir 仍会关闭；成员与根文件的 rm 一律 EROFS。
- **tab 生命周期**：tab 关闭后目录消失（ENOENT）；新开 tab 在下一次
  枚举即可看到。枚举即时返回缓存快照，list 事件在后台刷新，连续两次
  枚举间即可见新值。
- pagefs 与浏览器同信任级，只监听 localhost，不做认证。

## 代码结构

```
main.go                       CLI：serve / mount / umount / run / exec
internal/config/              进程级配置加载（pagefs.json / pagefs.yml）
internal/mount/               跨平台挂载适配器（darwin exec mount_nfs / linux syscall / windows 后置）
internal/provider/            Provider 接口（数据源契约）
internal/provider/cdp/        CDP 数据源实现（HTTP 发现 + WebSocket RPC）
internal/tree/                虚拟树：骨架（node/kind）、路径解析、tab 注册表、命令执行
internal/helper/              工具（词组 id 生成、SliceCache / MultiSliceCache）
```

架构与后续步骤（浏览器插件、Spool 落盘等）见 `.shapled/` 下的设计文档。

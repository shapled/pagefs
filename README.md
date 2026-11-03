# pagefs

把浏览器数据映射为本地文件系统，供 AI agents 与脚本通过普通的 `ls / cat / grep` 访问。体验对标 procfs/ftrace。

Step 1 通过 [CDP](https://chromedevtools.github.io/devtools-protocol/)（Chrome DevTools Protocol）暴露所有 tab 的列表、当前 DOM 与初始源码，挂载为**只读 NFSv3**（纯用户态，无需任何内核组件）。

```
$ cat /tmp/pagefs/tabs.list
elated-impala-1
Google 搜索
https://www.google.com/search?q=...
---
amber-jackal-2
GitHub
https://github.com/...

$ head -c 200 /tmp/pagefs/tabs/elated-impala-1/content
<html ...>...
```

## 前提

- Chrome 以调试端口启动（或对已运行的 Chrome 配置远程调试）：

  ```
  /Applications/Google Chrome.app/Contents/MacOS/Google Chrome --remote-debugging-port=9222
  ```

- Go 1.25+；macOS / Linux（Linux 挂载用 `mount.nfs`，参数类似）。

## 构建与运行

```
go build -o pagefs-bin .
./pagefs-bin                                  # CDP 默认 http://127.0.0.1:9222
./pagefs-bin -cdp http://127.0.0.1:9222 -listen 127.0.0.1:11111
```

启动后会打印挂载命令。

## 挂载（macOS，需要 root）

```
sudo mkdir -p /tmp/pagefs
sudo mount_nfs -o port=11111,mountport=11111,vers=3,tcp,nolocks,noacl,actimeo=0,noresvport \
     localhost:/ /tmp/pagefs
sudo umount /tmp/pagefs   # 卸载
```

`actimeo=0` 是必须的：关闭客户端属性缓存，否则会读到陈旧的目录列表和文件大小。

## 文件说明

| 路径                 | 内容                                                                                |
| -------------------- | ----------------------------------------------------------------------------------- |
| `/tabs.list`         | 所有 tab 的索引：三行一组（词组 id / 标题 / URL），组间 `---` 分隔                  |
| `/tabs/<id>/content` | 页面当前内容的 DOM 序列化（每次读取现取；HTML 是标记，JSON/文本是浏览器的渲染视图） |

`<id>` 是 pagefs 生成的词组 id（形如 `swift-otter-42`），分配后稳定不变，
与浏览器内部 id 的映射随 pagefs 进程存活；重启 pagefs 会重新分配。

## 行为说明

- **只读**：任何写入/删除都会被拒绝（EROFS）。
- **读时现取**：`content` 与 `tabs.list` 在读取时实时查询 CDP，每次返回
  当下最新的内容快照。
- **tab 生命周期**：tab 关闭后其目录立即消失（ENOENT）；新开 tab 在下一次
  `ls` 即可看到。
- pagefs 与浏览器同信任级，只监听 localhost，不做认证。

## 代码结构

```
main.go                       启动：CDP 探活 → NFSv3 serve
internal/provider/            Provider 接口（数据源契约）
internal/provider/cdp/        CDP 数据源实现（HTTP 发现 + WebSocket RPC）
internal/tree/                虚拟树：路径骨架、词组 id、billy 适配
internal/helper/              工具（词组 id 生成等）
```

架构与后续步骤（devtools 双视图、Firefox 扩展、Spool 落盘等）见 `docs/261102-ideas.md` 与 `docs/261102-step1.md`。

# pagefs

把浏览器数据映射为本地文件系统，供 AI agents 与脚本通过普通的 `ls / cat / echo / grep` 访问。体验对标 procfs/ftrace：**读 = 查询，写 = 命令**。

通过 [CDP](https://chromedevtools.github.io/devtools-protocol/)（Chrome DevTools Protocol）驱动 Chrome，挂载为 NFSv3（纯用户态，无需任何内核组件）。

```
$ cat /tmp/pagefs/tabs/list
tab-elated-impala-1
Google 搜索
https://www.google.com/search?q=...
---
tab-amber-jackal-2
GitHub
https://github.com/...

$ cat /tmp/pagefs/tabs/tab-amber-jackal-2/title
GitHub

$ echo "https://example.com" > /tmp/pagefs/tabs/open
$ cat /tmp/pagefs/tabs/open
ok
https://example.com
/tabs/tab-silver-otter-7
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

路径规则：**资源/成员**——动词与名词混排；实体 id 固定 `tab-` 前缀，
`ls /tabs/tab-*` 即纯 tab 枚举。

```
/help                          本文件系统的自述（目录结构 + 用法）
/tabs/list                     索引：三行一组（id / title / url），组间 ---
/tabs/open                     写 URL 开新 tab；响应第三行输出新 tab 路径
/tabs/tab-<id>/title           标题
/tabs/tab-<id>/url             URL
/tabs/tab-<id>/contentType     页面 MIME 类型
/tabs/tab-<id>/content         页面内容（HTML 取 outerHTML，其余取渲染文本）
/tabs/tab-<id>/screenshot.png  当前视口截图（PNG，读时现截）
/tabs/tab-<id>/navigate        写 URL 原地跳转（触发即回，后台加载）
/tabs/tab-<id>/reload          重载（触发即回，后台加载）
/tabs/tab-<id>/back|forward    历史导航（触发即回）
/tabs/tab-<id>/activate        置前台（会抢占桌面焦点）
/tabs/tab-<id>/close           关闭 tab
/tabs/tab-<id>/view-source     以源码视图打开本页当前 URL（响应两行：ok|err / 新 tab 路径）
/tabs/tab-<id>/evaljs          写 JS 表达式 → ok <JSON结果> / err
```

`<id>` 是 pagefs 生成的词组 id（形如 `tab-swift-otter-42`），分配后稳定不变，
与浏览器内部 id 的映射随 pagefs 进程存活；重启 pagefs 会重新分配。

## 行为说明

- **读 = 查询**：`content`、`screenshot.png`、元数据文件读取时现取，
  返回当下最新的快照。
- **写 = 命令**：命令文件写入即触发（含空内容，无参动词靠它）；写入
  `clear` 或 `clr` 是重置：还原为用法说明。响应写入同一文件：第一行
  `ok|err`，其后按命令回显请求原文（URL/表达式）并附输出或错误信息，
  末尾换行；触发词类命令（reload/close/view-source 等）不回显请求。
  命令触发即回（ok = 已受理），页面加载在后台进行，就绪与否由调用方
  自行查询（如 `evaljs` 写 `document.readyState`）。唯一的等待是
  evaljs 的 Promise 落定，超时（10s）回 err。
- **响应可读**：命令文件读回最近一次响应——`ok <payload>` 或
  `err <原因>`。命令执行与写入是异步防抖（300ms）或由下一次读取触发；
  `echo url > open && cat open` 即可获得结果。tab 关闭后其 close 文件
  仍可读到最后的响应。
- **tab 生命周期**：tab 关闭后目录消失（ENOENT）；新开 tab 在下一次
  枚举（`ls /tabs`、`cat /tabs/list`）即可看到。
- pagefs 与浏览器同信任级，只监听 localhost，不做认证。

## 代码结构

```
main.go                       启动：CDP 探活 → NFSv3 serve
internal/provider/            Provider 接口（数据源契约）
internal/provider/cdp/        CDP 数据源实现（HTTP 发现 + WebSocket RPC）
internal/tree/                虚拟树：骨架（node/kind）、路径解析、tab 注册表、命令执行
internal/helper/              工具（词组 id 生成等）
```

架构与后续步骤（命令行、浏览器插件、Spool 落盘等）见 `.shapled/` 下的设计文档。

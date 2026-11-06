package tree

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// 命令文件的执行机制。NFSv3 没有 close 语义，go-nfs 的 WRITE 又是逐 op
// 独立 open→seek→write→close，句柄与触发都靠不住，于是只有两条路：
//   - 观察冲刷：同路径的下一次 GETATTR / 读打开先执行挂起命令；
//   - 防抖定时：写入静默 cmdFlushDelay 后执行，覆盖 fire-and-forget。

// pendingCmd 是一条写入待执行的命令：状态按路径挂在树上。
type pendingCmd struct {
	t    *Tree
	path string
	n    *node

	mu       sync.Mutex
	buf      []byte
	done     bool
	finished chan struct{} // 响应落盘后关闭；并发触发者在此等结果
	timer    *time.Timer
}

func (t *Tree) ensurePending(path string, n *node) *pendingCmd {
	t.mu.Lock()
	defer t.mu.Unlock()
	if pc, ok := t.pending[path]; ok {
		return pc
	}
	pc := &pendingCmd{t: t, path: path, n: n, finished: make(chan struct{})}
	pc.timer = time.AfterFunc(cmdFlushDelay, func() { pc.exec() })
	t.pending[path] = pc
	return pc
}

// writePending 按写入偏移落位：go-nfs 每个 WRITE op 重开句柄并 Seek 到 offset，
// 顺序块的偏移即追加位置。写入 clear/clr = 重置节点（还原用法说明）；
// 其余内容（含空白）都是触发。
func (t *Tree) writePending(path string, n *node, off int64, data []byte) (int, error) {
	switch arg := strings.TrimSpace(string(data)); arg {
	case "clear", "clr":
		t.resetNode(path)
		return len(data), nil
	}
	pc := t.ensurePending(path, n)
	pc.mu.Lock()
	end := off + int64(len(data))
	if end > int64(len(pc.buf)) {
		pc.buf = append(pc.buf, make([]byte, end-int64(len(pc.buf)))...)
	}
	copy(pc.buf[off:end], data)
	pc.mu.Unlock()
	pc.timer.Reset(cmdFlushDelay)
	return len(data), nil
}

// resetNode 把节点还原为用法说明：放弃挂起命令并清除最近响应。
func (t *Tree) resetNode(path string) {
	t.resetPending(path)
	t.mu.Lock()
	delete(t.responses, path)
	t.mu.Unlock()
}

// resetPending 丢弃挂起命令：SETATTR 截断意味着放弃旧内容。
func (t *Tree) resetPending(path string) {
	t.mu.Lock()
	pc, ok := t.pending[path]
	if ok {
		delete(t.pending, path)
	}
	t.mu.Unlock()
	if ok {
		pc.cancel()
	}
}

// flushPending 冲刷挂起命令：调用方（stat/read）同步等响应落地。
func (t *Tree) flushPending(path string) {
	t.mu.Lock()
	pc, ok := t.pending[path]
	t.mu.Unlock()
	if ok {
		pc.exec()
	}
}

// cancel 放弃执行；已进入执行的命令不受影响。
func (pc *pendingCmd) cancel() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if pc.done {
		return
	}
	pc.done = true
	if pc.timer != nil {
		pc.timer.Stop()
	}
	close(pc.finished)
}

// exec 执行命令并落响应，幂等——并发触发时后者等前者完成再返回。
func (pc *pendingCmd) exec() {
	pc.mu.Lock()
	if pc.done {
		pc.mu.Unlock()
		<-pc.finished
		return
	}
	pc.done = true
	if pc.timer != nil {
		pc.timer.Stop()
	}
	data := pc.buf
	pc.mu.Unlock()

	pc.t.mu.Lock()
	if pc.t.pending[pc.path] == pc {
		delete(pc.t.pending, pc.path)
	}
	pc.t.mu.Unlock()

	// 响应 = 状态行 + 载荷；载荷在 execCmd 里按请求类型组装，
	// 请求原文有信息量（URL/表达式）才回显，无后期形态开关。
	status, payload := pc.t.execCmd(pc.path, pc.n, strings.TrimSpace(string(data)))
	resp := status + "\n" + payload
	if !strings.HasSuffix(resp, "\n") {
		resp += "\n"
	}

	pc.t.mu.Lock()
	pc.t.responses[pc.path] = resp
	pc.t.mu.Unlock()
	close(pc.finished)
}

// execCmd 执行单条命令，返回状态（ok/err）与载荷。载荷按请求类型组装：
// 请求原文有信息量（URL/表达式）才回显，其后是输出或原因；触发词类命令
// （argNone）不回显。
func (t *Tree) execCmd(path string, n *node, arg string) (string, string) {
	if n.kind == kCmdRoot { // tabs/open：开新 tab，应答前先分配 id
		if arg == "" {
			return "err", "empty url"
		}
		src, err := t.providers[0].OpenTab(t.srcCtx(), arg)
		if err != nil {
			return "err", arg + "\n" + err.Error()
		}
		t.refreshTabSeg(0, srcTimeout) // open 结果落在本源段：应答前单源刷新分配 id
		if s, ok := t.tabBySource(src); ok {
			// 第三行给完整路径：agent 拼上挂载点即可直接访问
			return "ok", arg + "\n/" + tabsName + "/" + s.id
		}
		return "err", arg + "\nopened but not listed"
	}

	// kCmdTab：执行时现查实体——arm 之后 tab 可能已经消失
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 3 {
		return "err", "bad path"
	}
	slot, ok := t.tabAlive(parts[1]) // 执行前确认实体在册（未命中复核）
	if !ok {
		return "err", "tab gone"
	}
	prov := t.providers[slot.pIx]
	src := slot.tab.ID
	switch n.arg {
	case skURL: // navigate
		if arg == "" {
			return "err", "empty url"
		}
		if err := prov.Navigate(t.srcCtx(), src, arg); err != nil {
			return "err", arg + "\n" + err.Error()
		}
		return "ok", arg
	case skExpr: // evaljs：结果 JSON 化
		v, err := prov.Eval(t.srcCtx(), src, arg)
		if err != nil {
			return "err", arg + "\n" + err.Error()
		}
		b, err := json.Marshal(v)
		if err != nil {
			return "err", arg + "\n" + fmt.Sprintf("%v", v)
		}
		return "ok", arg + "\n" + string(b)
	default: // argNone：写入任意内容触发（clear/clr 除外，已在写入侧拦截）
		var err error
		switch n.name {
		case "reload":
			err = prov.Reload(t.srcCtx(), src)
		case "back":
			err = prov.GoBack(t.srcCtx(), src)
		case "forward":
			err = prov.GoForward(t.srcCtx(), src)
		case "activate":
			err = prov.Activate(t.srcCtx(), src)
		case "close":
			err = prov.CloseTab(t.srcCtx(), src)
		case "view-source": // 结果是新 tab：应答前先触发 list 事件分配 id
			var nsrc string
			nsrc, err = prov.ViewSource(t.srcCtx(), src)
			if err == nil {
				t.refreshTabSeg(slot.pIx, srcTimeout)
				if s, ok := t.tabBySource(nsrc); ok {
					return "ok", "/" + tabsName + "/" + s.id
				}
				return "err", "opened but not listed"
			}
		default:
			return "err", "unknown verb " + n.name
		}
		if err != nil {
			return "err", err.Error()
		}
	}
	return "ok", ""
}

// cmdFile 是命令文件的 billy 句柄：写侧落位到 pending，读侧给打开时的
// 响应快照。Close 无语义（触发机制见文件头部注释）。
type cmdFile struct {
	t    *Tree
	path string
	n    *node
	pos  int64
	snap *bytes.Reader
}

func (f *cmdFile) Name() string { return f.path }

func (f *cmdFile) Write(p []byte) (int, error) {
	n, err := f.t.writePending(f.path, f.n, f.pos, p)
	f.pos += int64(n)
	return n, err
}

func (f *cmdFile) Truncate(int64) error {
	f.t.resetNode(f.path) // 截断 = 重置：还原用法说明
	f.pos = 0
	return nil
}

func (f *cmdFile) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		f.pos = off
	case io.SeekCurrent:
		f.pos += off
	case io.SeekEnd:
		f.pos = off
	}
	return f.pos, nil
}

func (f *cmdFile) snapshot() *bytes.Reader {
	if f.snap == nil {
		s, _ := f.t.response(f.path)
		f.snap = bytes.NewReader([]byte(s))
	}
	return f.snap
}

func (f *cmdFile) Read(p []byte) (int, error)              { return f.snapshot().Read(p) }
func (f *cmdFile) ReadAt(p []byte, off int64) (int, error) { return f.snapshot().ReadAt(p, off) }
func (f *cmdFile) Close() error                            { return nil }
func (f *cmdFile) Lock() error                             { return nil }
func (f *cmdFile) Unlock() error                           { return nil }

# HyperHand

让 Claude Code 通过 MCP 控制 Hyper-V Windows 虚拟机：截屏、鼠标、键盘（经 Hyper-V 主机侧，无需来宾配合），以及执行命令、传文件、剪贴板等（经来宾内的 agent，走 Hyper-V socket）。

- `hyperhand.exe`：主机托盘程序（需管理员权限），MCP 服务地址 `http://127.0.0.1:8770/mcp`（`-port` 可改端口），日志 `%LOCALAPPDATA%\HyperHand\hyperhand.log`。
- `hyperhand-agent.exe`：来宾内的 agent。

## 编译

```
go build -ldflags "-H windowsgui" -o build\hyperhand.exe .\cmd\hyperhand
go build -ldflags "-H windowsgui" -o build\hyperhand-agent.exe .\cmd\hyperhand-agent
```

## 安装

```
build\hyperhand.exe install
```

会弹一次 UAC，把两个 exe 复制到 `%LOCALAPPDATA%\HyperHand\`，创建登录时以最高权限运行的计划任务 `HyperHand` 并立即启动（托盘出现图标）。

## 接入 Claude Code

```
claude mcp add --transport http hyperhand http://127.0.0.1:8770/mcp
```

然后让 Claude 调用 `vm_install_agent`，把 agent 装进虚拟机（虚拟机需已开机并有用户登录）。

## 工具

所有工具都有可选参数 `vm`（虚拟机名，默认唯一正在运行的虚拟机）。

| 工具 | 说明 |
|---|---|
| vm_list / vm_start / vm_stop | 列出、启动、关闭虚拟机 |
| vm_screenshot | 截屏（PNG）；`source`: host（默认）或 agent |
| vm_click / vm_drag / vm_scroll | 鼠标点击、拖动、滚轮 |
| vm_type / vm_key | 输入文本（非 ASCII 走剪贴板粘贴）、按键/组合键 |
| vm_exec | 在来宾中执行命令，返回退出码、stdout、stderr |
| vm_push / vm_pull | 主机→来宾复制文件或目录；来宾→主机复制文件 |
| vm_clipboard_get / vm_clipboard_set | 读写来宾剪贴板 |
| vm_focus_window | 按标题激活窗口 |
| vm_wait | 等待进程退出/运行或文件出现 |
| vm_install_agent / vm_update_agent | 安装 / 更新来宾 agent |

带 agent 的工具（exec、文件、剪贴板、窗口、等待、agent 截屏）需先安装 agent。

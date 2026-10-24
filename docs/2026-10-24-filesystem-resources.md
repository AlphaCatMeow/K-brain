# 后端文件资源接口

认证后的 `POST /v1/fs` 为 LiveAgent 资源交互提供独立于模型运行的文件访问。Gateway 只转发固定类型，文件访问由 KB 执行。

## 配置

`kn backend -filesystem-root /path/to/projects` 可重复指定。未指定时仅暴露后端当前工作目录。请求中的 `workdir` 不会自动授予访问权；启动者需要增加需要浏览的根目录。

## 请求

- `{"action":"roots"}`：列出配置的根目录。
- `{"action":"list_dirs","path":"/path/to/projects","max_results":200}`：目录选择器。
- `{"action":"list","workdir":"/path/to/projects/demo","path":"src","depth":1,"offset":0,"max_results":200,"show_hidden":false}`：文件树。
- `{"action":"read_text","workdir":"/path/to/projects/demo","path":"README.md"}`：UTF-8 文本及内容摘要、大小、修改时间、行数。
- `read_preview`：返回 Base64 数据、MIME 类型、摘要、时间和大小，供文本及二进制预览使用，上限 2 MiB。
- `write_text`：`content`、`mode: "rewrite"`；覆盖现有文件必须同时提交此前读取的 `expected_mtime_ms` 和 `expected_content_hash`。
- `mkdir`、`delete`：创建目录、删除文件或空目录；不隐式递归删除非空目录。
- `rename`：`path` 与 `to_path` 均为工作空间相对路径，不覆盖目标。目前独占重命名支持 macOS/Linux，其他平台明确拒绝。

路径在 `os.Root` 范围内解析。文件读取额外限定在所选 workdir，拒绝符号链接逃逸。文本上限 2 MiB，深度上限 20，单页上限 10000。列表不跟随符号链接目录；暂未实现项目 ignore 规则。写操作串行，现有文件经版本校验后临时文件原子替换；新文件独占创建。外部进程可在最后校验与替换间修改文件，因此不宣称跨进程强事务。

## 验证与待办

路径隔离、认证、分页、隐藏文件、文本和取消测试通过。KB 全量测试、vet、backend/planning race 通过。Web 两档视口经 Gateway 实测目录选择、文件列表与文本读取。

文件编辑版本校验、原子写入、创建/重命名/删除和预览已补齐。Web 两档实际编辑 `brain.md` 并保存回读；`AGENTS.md`/`CLAUDE.md` 实际 WS 生命周期和陈旧写入拒绝通过。下一步仍需完整图片/文件引用、ignore、Windows 独占重命名与 SSH/SFTP/隧道迁移。不能声称完整文件管理或 SSH/隧道已由 KB 接管。

外部服务续测：Google 公共 ICS 已真实同步，隔离数据保存 30 个事件；私人 Google/iCloud/Exchange 账号和双向同步未验收。macOS debug 裸二进制通知测试明确要求 `.app` bundle；真实横幅未验收。图像后端 identity 与页面读取通过，但无真实生成模型。语音仍在 Gateway，未使用私人服务密钥。

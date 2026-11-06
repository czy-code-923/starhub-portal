# 星枢用户中心 0.10.2

- 修正 Linux 安装命令：root 不依赖 sudo；普通用户有 sudo 自动提权，无 sudo 提示 root 登录，保留所有命令参数。
- 全部 Linux 管理示例按 root 终端编写，入口不硬编码 IP，安装下载使用独立临时文件并清理。
- 新增 start / stop / restart / logs：后台运行、Linux Docker 与助手开机自启，保留数据库、管理员与原有设置，只管理本项目。
- 更新保留数据卸载后的恢复命令；明确 Windows Docker Desktop 登录启动与 systemd 的适用范围。
- 验证 root 无 sudo、非 root 有/无 sudo 的真实 POSIX 启动流程；隔离 Docker 验证停止、恢复、重启后会话与数据仍有效。

# 星枢用户中心 0.10.3

- 修复 Debian 等系统已有 Docker Engine、缺少 Compose 插件时安装中断：按系统架构获取 docker/compose 官方正式插件，验证 SHA-256 后原子安装到官方系统插件目录。
- 不重装或重启已有 Docker，不覆盖既有插件，不清理其他项目或数据。Windows 继续使用 Docker Desktop 提供的插件。
- 错误提示区分 docker info / docker compose version，构建错误显示本机诊断日志位置；安装打印四个阶段。
- 回归验证缺插件自动补齐、损坏包拒绝、已有文件保护；Linux CI 加载真实官方插件并验证完整安装、恢复与更新流程。

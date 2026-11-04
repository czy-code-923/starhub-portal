# 星枢用户中心 0.2.1

包含 0.2.0 的全部个人收款码、人工核款、可反复查看的邀请码、资料/密码维护、邮箱注册选项、权限副管理员及模拟套餐功能。

修复 systemd ProtectHome 启用时 Docker Buildx 尝试写入 /root/.docker 导致自动升级与回滚失败的问题。Docker 客户端状态改存本站私有目录，继续保留 ProtectHome 等服务保护；回滚同时恢复 Compose 定义，备份准备失败会启动原应用。

新增真实 systemd 沙箱中的 Docker 构建回归测试。安装器、正式包与完整源码已同步。

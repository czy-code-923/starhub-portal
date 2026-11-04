# 开发与手动构建

环境：Go 1.26+、Node.js 22.12+、PostgreSQL 17、Python 3.10+；部署容器需 Docker + Compose v2。

1. 启动 PostgreSQL，创建专用 `portal` 用户和空数据库。不要使用现有星枢的业务 schema。设置环境变量 `DATABASE_URL`、`MASTER_KEY`（32 随机字节的 hex，持久保存）、`PUBLIC_ORIGIN=http://127.0.0.1:18088`，可选 `LISTEN_ADDR=127.0.0.1:18088`。数据库迁移在启动时自动执行并受事务锁保护。
2. `cd web && npm ci && npm run build`。
3. 在项目根执行 `go build -trimpath -o portal ./cmd/portal`，再执行 `./portal admin-create you@example.com`，从标准输入传入 12 字节以上密码；不要把密码放入命令参数或 shell 历史。
4. `./portal` 启动 Go 服务；前端开发模式可在另一个终端执行 `cd web && npm run dev`，Vite 默认反代后端 18088。开发登录需要将 `PUBLIC_ORIGIN` 设置为实际浏览器入口（例如 `http://127.0.0.1:5173`）。

Python 生成主密钥示例：

```sh
python3 -c 'import secrets; print(secrets.token_hex(32))'
```

完整多阶段镜像：

```sh
docker build -t starhub-portal:local .
```

镜像运行需要以上环境变量，数据库由你单独准备；映射入口 `-p 127.0.0.1:18088:18088`，或使用安装器提供的 Compose 环境。不要将生产 `.env`、数据库、凭据和备份放进构建上下文。

分发包构建：先构建前端，再 `python3 scripts/package.py`，在 `dist` 下生成 Linux amd64 / arm64 包（Windows Docker Desktop 使用同一 Linux 包）、完整源码与 SHA256SUMS。`Runtime.Dockerfile` 使用包内预编译程序，VPS 不需要安装 Go 或 Node.js。

测试：

```sh
export TEST_DATABASE_URL='postgres://portal_test:local-test-password@127.0.0.1:5432/portal_test?sslmode=disable'
go vet ./...
go test -race -count=1 ./...
python3 scripts/test_manage.py
cd web && npm ci && npm test && npm run build
```

数据库集成测试生成随机隔离 schema，结束时删除自己的 schema，绝不对生产运行测试。测试包含邀请竞态、CSRF、角色隔离、他人订单/工单拒绝访问、优惠名额、幂等订单、支付回调、延迟到账、开通任务和密钥隔离。测试支付签名、邮件、星枢接入使用合成数据 / mock，不能替代真实商户沙箱验收。

Linux CI 还构建真实运行镜像，在临时独立目录运行全新安装 / 登录 / 更新助手 / 保留数据卸载检查。Windows 安装器包含环境检查，但需要在真实 Windows Docker Desktop 上另行验收。

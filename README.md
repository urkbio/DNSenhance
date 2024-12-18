# DNSenhance

DNSenhance 是一个简单的 DNS 转发器，具有缓存、分流、拦截和实时监控功能。它能够区分国内外域名，提供更快的 DNS 解析服务，并通过直观的 Web 界面展示运行状态和查询日志。

## 快速开始

### 安装

```
bash
git clone https://github.com/urkbio/DNSenhance.git
cd DNSenhance
go build
```

默认监听端口：
- DNS 服务：53 (UDP/TCP)
- Web 界面：8080

### 配置

程序会自动在当前目录下查找以下配置文件：
- `config.json`: 配置档
- `domains.txt`: 国内域名列表
- `block.txt`: 需要拦截的域名列表

## Web 界面

访问 `http://localhost:8080` 可以查看：
- 实时 QPS
- 运行时间
- 缓存命中率
- 查询分布
- 域名拦截统计
- 实时查询日志

## 技术栈

- 后端：Go
- 前端：HTML5, CSS3, JavaScript
- 图表：Chart.js
- 图标：Material Design Icons

## 系统要求

- Go 1.16 或更高版本
- 支持 Windows, Linux, macOS

## 注意事项

1. 运行服务器需要管理员/root 权限（因为需要使用 53 端口）
2. 确保 53 端口未被其他 DNS 服务占用
3. 建议在本地环境或内网使用

## 许可证

MIT License

## 贡献

欢迎提交 Issue 和 Pull Request！

## 致谢

感谢以下开源项目：
- [miekg/dns](https://github.com/miekg/dns)
- [Chart.js](https://www.chartjs.org/)
- [Material Design Icons](https://materialdesignicons.com/)

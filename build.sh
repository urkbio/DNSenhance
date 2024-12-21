#!/bin/bash
echo "=== 开始构建 DNSenhance ==="

# 创建必要的目录
mkdir -p configs
mkdir -p data
mkdir -p logs
mkdir -p web/static/images

# 复制配置文件（如果不存在）
[ ! -f configs/config.json ] && cp examples/config.json configs/
[ ! -f configs/domains.txt ] && cp examples/domains.txt configs/
[ ! -f configs/block.txt ] && cp examples/block.txt configs/

# 复制静态文件
cp -r web/* web/

# 编译程序
echo "正在编译..."
cd cmd
go build -o ../dnsenhance
cd ..

if [ $? -ne 0 ]; then
    echo "编译失败！"
    exit 1
fi

# 设置执行权限
chmod +x dnsenhance

echo "=== 构建完成 ==="
echo "运行 ./dnsenhance 启动服务器" 
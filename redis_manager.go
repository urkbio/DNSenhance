package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/go-redis/redis/v8"
)

type RedisManager struct {
	cmd     *exec.Cmd
	client  *redis.Client
	dataDir string
	port    int
	exePath string
}

func NewRedisManager(port int) (*RedisManager, error) {
	// 获取程序运行目录
	exePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("获取程序路径失败: %v", err)
	}
	programDir := filepath.Dir(exePath)

	// 构建 Redis 目录路径
	redisDir := filepath.Join(programDir, "redis")
	redisExe := filepath.Join(redisDir, "redis-server.exe")

	// 检查 Redis 可执行文件和必要的 DLL 是否存在
	requiredFiles := []string{
		"redis-server.exe",
		"msvcr120.dll", // Visual C++ 2013 Runtime
		"msvcp120.dll", // Visual C++ 2013 Runtime
	}

	for _, file := range requiredFiles {
		path := filepath.Join(redisDir, file)
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("Redis所需文件 %s 不存在: %v", file, err)
		}
	}

	// 创建数据目录
	dataDir := filepath.Join(redisDir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("创建Redis数据目录失败: %v", err)
	}

	// 设置环境变量
	os.Setenv("PATH", redisDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return &RedisManager{
		dataDir: dataDir,
		port:    port,
		exePath: redisExe,
	}, nil
}

func (rm *RedisManager) Start() error {
	// 启动 Redis 服务器
	rm.cmd = exec.Command(rm.exePath,
		"--port", fmt.Sprintf("%d", rm.port),
		"--dir", rm.dataDir,
		"--dbfilename", "dump.rdb",
		"--daemonize", "no",
		"--protected-mode", "no",
		"--bind", "127.0.0.1",
		"--maxmemory", "100mb",
		"--maxmemory-policy", "allkeys-lru",
		"--loglevel", "verbose",
	)

	// 设置工作目录
	rm.cmd.Dir = filepath.Dir(rm.exePath)

	// 创建日志文件
	logFile, err := os.OpenFile(filepath.Join(rm.dataDir, "redis.log"),
		os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("创建Redis日志文件失败: %v", err)
	}

	// 写入启动信息到日志
	fmt.Fprintf(logFile, "=== Redis启动时间: %s ===\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(logFile, "Redis路径: %s\n", rm.exePath)
	fmt.Fprintf(logFile, "数据目录: %s\n", rm.dataDir)
	fmt.Fprintf(logFile, "端口: %d\n", rm.port)
	fmt.Fprintf(logFile, "环境变量PATH: %s\n", os.Getenv("PATH"))

	rm.cmd.Stdout = logFile
	rm.cmd.Stderr = logFile

	fmt.Printf("正在启动Redis: %s\n", rm.exePath)
	if err := rm.cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("启动Redis失败: %v", err)
	}

	// 创建一个通道来接收进程退出信息
	done := make(chan error, 1)
	go func() {
		done <- rm.cmd.Wait()
	}()

	// 创建 Redis 客户端
	rm.client = redis.NewClient(&redis.Options{
		Addr:        fmt.Sprintf("localhost:%d", rm.port),
		DB:          0,
		DialTimeout: 5 * time.Second,
	})

	// 等待 Redis 启动并测试连接
	ctx := context.Background()
	fmt.Println("等待Redis服务就绪...")

	for i := 0; i < 20; i++ {
		select {
		case err := <-done:
			logFile.Close()
			return fmt.Errorf("Redis进程意外退出: %v", err)
		default:
			if err := rm.client.Ping(ctx).Err(); err == nil {
				fmt.Println("Redis服务已就绪")
				return nil
			}
			fmt.Printf("尝试连接Redis (%d/20)...\n", i+1)
			time.Sleep(1 * time.Second)
		}
	}

	logFile.Close()
	if rm.cmd.Process != nil {
		rm.cmd.Process.Kill()
	}
	return fmt.Errorf("Redis服务启动超时，请检查日志文件")
}

func (rm *RedisManager) Stop() error {
	if rm.client != nil {
		if err := rm.client.Close(); err != nil {
			fmt.Printf("���告: 关闭Redis客户端失败: %v\n", err)
		}
	}

	if rm.cmd != nil && rm.cmd.Process != nil {
		fmt.Println("正在停止Redis服务...")
		if err := rm.cmd.Process.Kill(); err != nil {
			return fmt.Errorf("停止Redis服务失败: %v", err)
		}
		rm.cmd.Wait()
	}

	return nil
}

func (rm *RedisManager) GetClient() *redis.Client {
	return rm.client
}

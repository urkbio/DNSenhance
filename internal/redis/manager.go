package redis

import (
	"context"
	"fmt"

	"github.com/go-redis/redis/v8"
)

type Manager struct {
	client *redis.Client
	port   int
}

func NewManager(port int) (*Manager, error) {
	return &Manager{
		port: port,
	}, nil
}

func (m *Manager) Start() error {
	m.client = redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("localhost:%d", m.port),
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	// 测试连接
	ctx := context.Background()
	if err := m.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("Redis连接失败: %v", err)
	}

	return nil
}

func (m *Manager) Stop() error {
	if m.client != nil {
		if err := m.client.Close(); err != nil {
			return fmt.Errorf("Redis关闭失败: %v", err)
		}
	}
	return nil
}

func (m *Manager) GetClient() *redis.Client {
	return m.client
}

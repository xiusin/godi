// Copyright All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// Package main 示例：基础使用 —— 注册/获取单例、工厂函数、生命周期回调
package main

import (
	"fmt"
	"reflect"
	"sync/atomic"

	"github.com/xiusin/godi"
)

// DB 数据库连接（单例）
type DB struct{ DSN string }

// Query 模拟查询
func (d *DB) Query(sql string) string {
	return fmt.Sprintf("result from %s: %s", d.DSN, sql)
}

// Logger 接口（byType 自动装配）
type Logger interface {
	Info(msg string)
	Warn(msg string)
}

// StdLogger 标准输出日志
type StdLogger struct{}

func (StdLogger) Info(msg string) { fmt.Println("[INFO]", msg) }
func (StdLogger) Warn(msg string) { fmt.Println("[WARN]", msg) }

// UserService 用户服务
type UserService struct {
	// 导出字段按类型自动注入
	DB  *DB
	Log Logger

	userCount atomic.Int64
}

// AfterPropertiesSet 初始化回调（实现 InitializingBean）
func (u *UserService) AfterPropertiesSet() error {
	u.Log.Info("UserService init: pinging DB...")
	u.Log.Info(u.DB.Query("SELECT 1"))
	return nil
}

// Destroy 销毁回调（实现 DisposableBean）
func (u *UserService) Destroy() error {
	u.Log.Info("UserService shutdown: cleaning up...")
	return nil
}

// GetUserCount 模拟业务方法
func (u *UserService) GetUserCount() int64 {
	u.userCount.Add(1)
	return u.userCount.Load()
}

func main() {
	f := godi.NewDefaultBeanFactory()

	// 注册 DB
	f.RegisterBeanDefinition("db", &godi.BeanDefinition{
		Name:  "db",
		Type:  reflect.TypeOf((*DB)(nil)),
		Scope: godi.ScopeSingleton,
		Factory: func(_ godi.BeanFactory) (any, error) {
			return &DB{DSN: "mysql://root:pass@localhost:3306/test"}, nil
		},
	})

	// 注册 Logger（标记为 Primary，byType 时优先选中）
	// 注意：Type 必须设置为接口类型，以便 byType 查找时能正确匹配
	f.RegisterBeanDefinition("logger", &godi.BeanDefinition{
		Name:    "logger",
		Type:    reflect.TypeOf((*Logger)(nil)).Elem(),
		Scope:   godi.ScopeSingleton,
		Primary: true,
		Factory: func(_ godi.BeanFactory) (any, error) { return StdLogger{}, nil },
	})

	// 注册 UserService：AutowireByType 自动按类型注入字段
	f.RegisterBeanDefinition("userService", &godi.BeanDefinition{
		Name:     "userService",
		Type:     reflect.TypeOf((*UserService)(nil)),
		Scope:    godi.ScopeSingleton,
		Autowire: godi.AutowireByType,
		Factory: func(_ godi.BeanFactory) (any, error) {
			return &UserService{}, nil
		},
	})

	// 预实例化所有非懒加载单例
	fmt.Println("=== Refreshing context ===")
	if err := f.PreInstantiateSingletons(); err != nil {
		panic(err)
	}

	// 使用
	fmt.Println("\n=== Using beans ===")
	// 泛型 API：按类型获取（无需类型断言）
	svc, _ := godi.GetBeanT[*UserService](f)
	// 泛型 API：按名称获取（同样无需类型断言）
	db := godi.MustGetBeanByNameT[*DB](f, "db")
	fmt.Printf("DB DSN: %s\n", db.DSN)
	fmt.Printf("User count: %d\n", svc.GetUserCount())
	fmt.Printf("User count: %d\n", svc.GetUserCount())

	// 关闭
	fmt.Println("\n=== Closing context ===")
	if err := f.DestroySingletons(); err != nil {
		panic(err)
	}
}

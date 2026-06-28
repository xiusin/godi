// Copyright All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// Package main 示例：BeanPostProcessor 实现日志装饰器（AOP 基础）
//
// 演示如何用 BeanPostProcessor 在 bean 初始化前后介入，
// 并用装饰器模式替换原 bean（类似 Spring AOP 动态代理）。
package main

import (
	"fmt"
	"time"

	"github.com/xiusin/godi"
)

// UserService 用户服务接口
type UserService interface {
	GetUser(id int) string
}

// UserServiceImpl 真实实现
type UserServiceImpl struct {
	DB string // 模拟依赖
}

func (u *UserServiceImpl) GetUser(id int) string {
	time.Sleep(10 * time.Millisecond) // 模拟耗时
	return fmt.Sprintf("user_%d", id)
}

// LoggingProxy 日志装饰器代理
type LoggingProxy struct {
	target UserService
	name   string
}

func (p *LoggingProxy) GetUser(id int) string {
	start := time.Now()
	result := p.target.GetUser(id)
	fmt.Printf("[LOG] %s.GetUser(%d) took %v\n", p.name, id, time.Since(start))
	return result
}

// LoggingPostProcessor 日志后置处理器
// 实现 BeanPostProcessor 接口，在 after 阶段用代理替换原 bean
type LoggingPostProcessor struct{}

func (LoggingPostProcessor) Order() int { return 10 }

func (LoggingPostProcessor) PostProcessBeforeInitialization(bean any, name string) (any, error) {
	fmt.Printf("[PP] Before init: %s (%T)\n", name, bean)
	return bean, nil
}

func (LoggingPostProcessor) PostProcessAfterInitialization(bean any, name string) (any, error) {
	fmt.Printf("[PP] After init: %s (%T)\n", name, bean)

	// 对 UserService 接口的 bean 应用日志装饰器
	if svc, ok := bean.(UserService); ok {
		fmt.Printf("[PP] Wrapping %s with LoggingProxy\n", name)
		return &LoggingProxy{target: svc, name: name}, nil
	}
	return bean, nil
}

func main() {
	f := godi.NewDefaultBeanFactory()

	// 注册 PostProcessor（必须在业务 bean 之前注册才能生效）
	f.AddBeanPostProcessor(LoggingPostProcessor{})

	// 注册业务 bean
	f.RegisterBeanDefinition("userService", &godi.BeanDefinition{
		Name:  "userService",
		Scope: godi.ScopeSingleton,
		Factory: func(_ godi.BeanFactory) (any, error) {
			return &UserServiceImpl{DB: "mysql://..."}, nil
		},
	})

	fmt.Println("=== Getting bean ===")
	svc, err := f.GetBean("userService")
	if err != nil {
		panic(err)
	}

	fmt.Println("\n=== Calling service ===")
	userSvc := svc.(UserService)
	user := userSvc.GetUser(100)
	fmt.Printf("Result: %s\n", user)
}

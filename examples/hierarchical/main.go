// Copyright All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// Package main 示例：父子容器（HierarchicalBeanFactory）
//
// 演示父子容器层级：
// - 父容器放共享服务（DB, Config）
// - 子容器放请求级服务
// - 子容器找不到时委托父容器
package main

import (
	"fmt"
	"reflect"

	"github.com/xiusin/godi"
)

// DB 数据库（父容器共享）
type DB struct{ Name string }

// Config 配置（父容器共享）
type Config struct{ Env string }

// RequestHandler 请求处理器（子容器特有）
type RequestHandler struct {
	DB     *DB
	Config *Config
	Path   string
}

func (r *RequestHandler) Handle() string {
	return fmt.Sprintf("[%s] handling %s with %s", r.Config.Env, r.Path, r.DB.Name)
}

func main() {
	// ========== 父容器：放全局共享服务 ==========
	parent := godi.NewDefaultBeanFactory()

	parent.RegisterBeanDefinition("db", &godi.BeanDefinition{
		Name:  "db",
		Type:  reflect.TypeOf((*DB)(nil)),
		Scope: godi.ScopeSingleton,
		Factory: func(_ godi.BeanFactory) (any, error) {
			return &DB{Name: "main_db"}, nil
		},
	})

	parent.RegisterBeanDefinition("config", &godi.BeanDefinition{
		Name:  "config",
		Type:  reflect.TypeOf((*Config)(nil)),
		Scope: godi.ScopeSingleton,
		Factory: func(_ godi.BeanFactory) (any, error) {
			return &Config{Env: "production"}, nil
		},
	})

	// ========== 子容器：放请求级服务，继承父容器 ==========
	child := godi.NewDefaultBeanFactory(parent)

	child.RegisterBeanDefinition("handler", &godi.BeanDefinition{
		Name:     "handler",
		Type:     reflect.TypeOf((*RequestHandler)(nil)),
		Scope:    godi.ScopePrototype,
		Autowire: godi.AutowireByType,
		Factory: func(_ godi.BeanFactory) (any, error) {
			return &RequestHandler{Path: "/api/users"}, nil
		},
	})

	fmt.Println("=== Parent container ===")
	db, _ := parent.GetBean("db")
	fmt.Printf("Parent has DB: %v\n", db.(*DB).Name)

	_, err := parent.GetBean("handler")
	fmt.Printf("Parent has handler? err=%v\n", err != nil) // true，父容器看不到子容器

	fmt.Println("\n=== Child container ===")
	handler, _ := child.GetBean("handler")
	h := handler.(*RequestHandler)
	fmt.Printf("Child handler: %s\n", h.Handle())

	fmt.Println("\n✓ Child sees parent's DB & Config, parent doesn't see child's handler")
}

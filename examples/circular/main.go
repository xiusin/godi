// Copyright All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// Package main 示例：循环依赖自动解决（三级缓存）
//
// 演示 godi 的三级缓存机制如何自动解决 setter/field 注入的循环依赖。
// 两个 bean 互相引用对方，容器能自动完成注入。
package main

import (
	"fmt"
	"reflect"

	"github.com/xiusin/godi"
)

// Husband 丈夫（依赖 Wife）
type Husband struct {
	Name string
	Wife *Wife
}

// Wife 妻子（依赖 Husband）
type Wife struct {
	Name    string
	Husband *Husband
}

func main() {
	f := godi.NewDefaultBeanFactory()

	// 注册 Husband：按类型自动装配 Wife 字段
	f.RegisterBeanDefinition("husband", &godi.BeanDefinition{
		Name:     "husband",
		Type:     reflect.TypeOf((*Husband)(nil)),
		Scope:    godi.ScopeSingleton,
		Autowire: godi.AutowireByType,
		Factory: func(_ godi.BeanFactory) (any, error) {
			return &Husband{Name: "Tom"}, nil
		},
	})

	// 注册 Wife：按类型自动装配 Husband 字段
	f.RegisterBeanDefinition("wife", &godi.BeanDefinition{
		Name:     "wife",
		Type:     reflect.TypeOf((*Wife)(nil)),
		Scope:    godi.ScopeSingleton,
		Autowire: godi.AutowireByType,
		Factory: func(_ godi.BeanFactory) (any, error) {
			return &Wife{Name: "Jerry"}, nil
		},
	})

	// 获取 Husband
	h, _ := f.GetBean("husband")
	husband := h.(*Husband)

	fmt.Printf("Husband: %s\n", husband.Name)
	fmt.Printf("Husband's wife: %s\n", husband.Wife.Name)
	fmt.Printf("Wife's husband: %s\n", husband.Wife.Husband.Name)

	// 验证环形引用成立：Husband.Wife.Husband == Husband
	if husband.Wife.Husband == husband {
		fmt.Println("\n✓ Circular dependency resolved! Husband.Wife.Husband === Husband")
	} else {
		fmt.Println("\n✗ Circular dependency NOT resolved")
	}
}

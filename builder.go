// Copyright All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package godi

import (
	"reflect"
	"sync"
)

// AbstractServiceProvider 服务提供者接口，用于模块化注册一批 bean。
// 对应 Spring 的 @Configuration / BeanDefinitionRegistry 后置注册。
type AbstractServiceProvider interface {
	Register(BeanFactory)
}

// ---- 全局默认 BeanFactory ----

var (
	defaultFactory     = NewDefaultBeanFactory()
	defaultFactoryOnce sync.Once
)

// GetDefaultFactory 返回全局默认 BeanFactory
func GetDefaultFactory() *DefaultBeanFactory { return defaultFactory }

// ---- 包级便捷 API（操作全局默认容器）----
// 对应 Spring 的静态访问便利，但底层均为 BeanFactory 实例方法。

// RegisterBean 注册 bean 定义到全局容器
func RegisterBean(name string, bd *BeanDefinition) {
	defaultFactory.RegisterBeanDefinition(name, bd)
}

// RegisterSingleton 以单例工厂注册（便捷：等价于 singleton scope + Factory）
func RegisterSingleton(name string, supplier SupplierFunc) *BeanDefinition {
	bd := &BeanDefinition{Name: name, Scope: ScopeSingleton, Factory: supplier}
	defaultFactory.RegisterBeanDefinition(name, bd)
	return bd
}

// RegisterPrototype 以原型工厂注册
func RegisterPrototype(name string, supplier SupplierFunc) *BeanDefinition {
	bd := &BeanDefinition{Name: name, Scope: ScopePrototype, Factory: supplier}
	defaultFactory.RegisterBeanDefinition(name, bd)
	return bd
}

// RegisterInstance 注册一个已存在的实例为单例（对应 Spring 静态 bean）
func RegisterInstance(name string, instance any) *BeanDefinition {
	bd := &BeanDefinition{
		Name:    name,
		Scope:   ScopeSingleton,
		Type:    reflect.TypeOf(instance),
		Factory: func(_ BeanFactory) (any, error) { return instance, nil },
	}
	defaultFactory.RegisterBeanDefinition(name, bd)
	return bd
}

// RegisterAlias 注册别名
func RegisterAlias(alias, name string) { defaultFactory.RegisterAlias(alias, name) }

// GetBean 从全局容器获取 bean
func GetBean(name string) (any, error) { return defaultFactory.GetBean(name) }

// MustGetBean 获取 bean，失败 panic
func MustGetBean(name string) any {
	v, err := defaultFactory.GetBean(name)
	if err != nil {
		panic(err)
	}
	return v
}

// ContainsBean 全局容器是否包含 bean
func ContainsBean(name string) bool { return defaultFactory.ContainsBean(name) }

// GetBeanByType 全局容器按类型获取
func GetBeanByType(t reflect.Type) (any, error) { return defaultFactory.GetBeanByType(t) }

// GetBeansOfType 全局容器获取某类型所有 bean
func GetBeansOfType(t reflect.Type) (map[string]any, error) {
	return defaultFactory.GetBeansOfType(t)
}

// AddBeanPostProcessor 注册后置处理器到全局容器
func AddBeanPostProcessor(p BeanPostProcessor) { defaultFactory.AddBeanPostProcessor(p) }

// RegisterScope 注册自定义作用域到全局容器
func RegisterScope(name string, scope Scope) { defaultFactory.RegisterScope(name, scope) }

// RegisterProviders 批量注册服务提供者
func RegisterProviders(providers ...AbstractServiceProvider) {
	for _, p := range providers {
		p.Register(defaultFactory)
	}
}

// Refresh 预实例化全局容器中所有非懒加载单例（对应 Spring ApplicationContext.refresh）
func Refresh() error { return defaultFactory.PreInstantiateSingletons() }

// Close 销毁全局容器所有单例（对应 Spring ApplicationContext.close）
func Close() error { return defaultFactory.DestroySingletons() }

// ---- 泛型方法（Go 1.27+），消除调用方类型断言 ----
//
// 利用 Go 1.27 泛型方法特性（提案 #77273），将这些 API 声明为 *DefaultBeanFactory 的方法，
// 而非包级泛型函数。调用方式更符合面向对象风格，支持链式调用。
//
// 注意：泛型方法不能用于匹配接口（Go 语言约束），因此这些方法定义在具体类型
// *DefaultBeanFactory 上，而非 BeanFactory 接口上。BeanFactory 接口仍保留非泛型方法。

// GetBeanT 按【类型】类型安全地获取 bean。T 应为指针或接口类型。
// 等价于 GetBeanByType，但省去 reflect.TypeOf 与类型断言。
func (f *DefaultBeanFactory) GetBeanT[T any]() (T, error) {
	var zero T
	t := reflect.TypeOf((*T)(nil)).Elem()
	v, err := f.GetBeanByType(t)
	if err != nil {
		return zero, err
	}
	ct, ok := v.(T)
	if !ok {
		return zero, ErrBeanNotOfRequiredType
	}
	return ct, nil
}

// MustGetBeanT 按【类型】类型安全地获取 bean，失败 panic。
func (f *DefaultBeanFactory) MustGetBeanT[T any]() T {
	v, err := f.GetBeanT[T]()
	if err != nil {
		panic(err)
	}
	return v
}

// GetBeanByNameT 按【名称】类型安全地获取 bean，省去 GetBean(name) 后的类型断言。
func (f *DefaultBeanFactory) GetBeanByNameT[T any](name string) (T, error) {
	var zero T
	v, err := f.GetBean(name)
	if err != nil {
		return zero, err
	}
	ct, ok := v.(T)
	if !ok {
		return zero, ErrBeanNotOfRequiredType
	}
	return ct, nil
}

// MustGetBeanByNameT 按【名称】类型安全地获取 bean，失败 panic。
func (f *DefaultBeanFactory) MustGetBeanByNameT[T any](name string) T {
	v, err := f.GetBeanByNameT[T](name)
	if err != nil {
		panic(err)
	}
	return v
}

// GetBeansT 获取某类型的所有 bean（泛型集合注入，对应 Spring List<T> 注入）。
// T 通常为接口类型，返回所有实现该接口的 bean。
func (f *DefaultBeanFactory) GetBeansT[T any]() (map[string]T, error) {
	t := reflect.TypeOf((*T)(nil)).Elem()
	raw, err := f.GetBeansOfType(t)
	if err != nil {
		return nil, err
	}
	out := make(map[string]T, len(raw))
	for k, v := range raw {
		cv, ok := v.(T)
		if !ok {
			return nil, ErrBeanNotOfRequiredType
		}
		out[k] = cv
	}
	return out, nil
}

// ---- 泛型包级便捷 API（操作全局默认容器，底层调用 *DefaultBeanFactory 的泛型方法）----

// GetBeanT 按【类型】类型安全地从全局容器获取 bean。
// 等价于 defaultFactory.GetBeanT[T]()。
func GetBeanT[T any]() (T, error) { return defaultFactory.GetBeanT[T]() }

// MustGetBeanT 按【类型】类型安全地从全局容器获取 bean，失败 panic。
func MustGetBeanT[T any]() T { return defaultFactory.MustGetBeanT[T]() }

// GetBeanByNameT 按【名称】类型安全地从全局容器获取 bean。
// 等价于 defaultFactory.GetBeanByNameT[T](name)。
func GetBeanByNameT[T any](name string) (T, error) {
	return defaultFactory.GetBeanByNameT[T](name)
}

// MustGetBeanByNameT 按【名称】类型安全地从全局容器获取 bean，失败 panic。
func MustGetBeanByNameT[T any](name string) T {
	return defaultFactory.MustGetBeanByNameT[T](name)
}

// GetBeansT 从全局容器获取某类型的所有 bean（泛型集合注入）。
func GetBeansT[T any]() (map[string]T, error) { return defaultFactory.GetBeansT[T]() }


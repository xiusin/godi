// Copyright All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package godi

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// Disposable 生命周期接口；单例实例实现该接口时，容器 Close 会自动调用 Dispose 释放资源
type Disposable interface {
	Dispose() error
}

// Definition 服务定义，描述服务的构造方式与作用域。
//
// 不再内嵌 sync.Mutex，避免把 Lock/Unlock 暴露为公开 API（封装修复）。
type Definition struct {
	name          string
	typeName      string
	shared        bool
	factory       BuildHandler
	paramsFactory BuildWithHandler

	// 单例构造协调
	mu       sync.Mutex
	instance any
	resolved atomic.Bool // 用标志位代替 instance != nil，允许工厂合法返回 nil
}

func (d *Definition) TypeName() string { return d.typeName }

func (d *Definition) SetTypeName(call func() string) { d.typeName = call() }

func (d *Definition) SetShared(shared bool) { d.shared = shared }

func (d *Definition) ServiceName() string { return d.name }

func (d *Definition) IsSingleton() bool { return d.shared }

// IsResolved 是否已构造（单例）。用原子标志位判断，允许工厂返回 nil 实例
func (d *Definition) IsResolved() bool { return d.resolved.Load() }

// resolve 解析服务实例。
//   - 单例：双重检查 + 锁，保证只构造一次；构造失败不缓存，允许后续重试
//   - 瞬态：无锁调用 factory，支持高并发
//
// 循环依赖检测由 resolveScope.getByName 在加锁前完成，避免持锁期间重入死锁。
// 写者执行 d.instance = s 后再 d.resolved.Store(true)，读者先 d.resolved.Load()
// 再读 d.instance，经 atomic 的 release/acquire 建立 happens-before，无锁快路径安全。
func (d *Definition) resolve(scope *resolveScope) (any, error) {
	if !d.shared {
		if d.factory == nil {
			return nil, fmt.Errorf("%w: %s", ErrServiceNotExists, d.name)
		}
		return d.factory(scope) // 瞬态：无锁并发
	}
	// 无锁快路径
	if d.resolved.Load() {
		return d.instance, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.resolved.Load() { // 双重检查
		return d.instance, nil
	}
	if d.factory == nil {
		return nil, fmt.Errorf("%w: %s", ErrServiceNotExists, d.name)
	}
	s, err := d.factory(scope)
	if err != nil {
		return nil, err // 不吞错；失败不缓存，允许重试
	}
	d.instance = s
	d.resolved.Store(true)
	return s, nil
}

func (d *Definition) resolveWithParams(scope *resolveScope, params ...any) (any, error) {
	if d.paramsFactory == nil {
		return nil, fmt.Errorf("%w: %s", ErrServiceNotExists, d.name)
	}
	return d.paramsFactory(scope, params...)
}

// dispose 释放单例资源
func (d *Definition) dispose() error {
	if !d.shared || !d.resolved.Load() {
		return nil
	}
	if c, ok := d.instance.(Disposable); ok {
		return c.Dispose()
	}
	return nil
}

func NewDefinition(name string, factory BuildHandler, shared bool) *Definition {
	return &Definition{
		name:    name,
		factory: factory,
		shared:  shared,
	}
}

// NewParamsDefinition 带参服务：每次调用都重新构造，不是单例
// （修复原实现把 shared 置 true 导致 GetWithParams 永远报 ErrServiceSingleton 的死链路）
func NewParamsDefinition(name string, factory BuildWithHandler) *Definition {
	return &Definition{
		name:          name,
		paramsFactory: factory,
		shared:        false,
	}
}

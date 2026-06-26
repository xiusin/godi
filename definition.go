// Copyright All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package godi

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// Disposable 销毁回调接口；单例实例实现该接口时，容器 Close 会自动调用 Dispose。
// 对应 Spring DisposableBean + destroy-method。
type Disposable interface {
	Dispose() error
}

// Initializer 初始化回调接口；实例构造成功后、缓存前自动调用 Init。
// 对应 Spring InitializingBean.afterPropertiesSet + init-method。
// Init 失败则单例不缓存（允许重试），瞬态则直接返回错误。
type Initializer interface {
	Init() error
}

// Definition 服务定义，描述服务的构造方式、作用域与生命周期。
//
// 不再内嵌 sync.Mutex，避免把 Lock/Unlock 暴露为公开 API（封装修复）。
// shared / lazy 用 atomic.Bool，使运行期 SetShared/SetLazy 与 resolve 之间无数据竞争。
type Definition struct {
	name          string
	typeName      string
	shared        atomic.Bool // 作用域标志，原子读写避免 SetShared 竞争
	lazy          atomic.Bool // 懒加载标志：true=首次Get才构造，false=PreWarm预热
	factory       BuildHandler
	paramsFactory BuildWithHandler

	// 单例构造协调
	mu       sync.Mutex
	instance any
	resolved atomic.Bool // 用标志位代替 instance != nil，允许工厂合法返回 nil
}

func (d *Definition) TypeName() string { return d.typeName }

func (d *Definition) SetTypeName(call func() string) { d.typeName = call() }

// SetShared 原子地修改作用域。建议仅在注册阶段调用。
func (d *Definition) SetShared(shared bool) { d.shared.Store(shared) }

// SetLazy 设置懒加载：true（默认）首次 Get 才构造；false 则 PreWarm 时预热。
// 对应 Spring bean 的 lazy-init 属性。
func (d *Definition) SetLazy(lazy bool) { d.lazy.Store(lazy) }

func (d *Definition) IsLazy() bool { return d.lazy.Load() }

func (d *Definition) ServiceName() string { return d.name }

func (d *Definition) IsSingleton() bool { return d.shared.Load() }

// IsResolved 是否已构造（单例）。用原子标志位判断，允许工厂返回 nil 实例
func (d *Definition) IsResolved() bool { return d.resolved.Load() }

// resolve 解析服务实例。
//   - 单例：双重检查 + 锁，保证只构造一次；构造失败不缓存，允许后续重试
//   - 瞬态：无锁调用 factory，支持高并发
//   - 构造成功后调用 Init()（若实现 Initializer），失败则不缓存
//
// 循环依赖检测由 resolveScope 在加锁前完成，避免持锁期间重入死锁。
func (d *Definition) resolve(scope *resolveScope) (any, error) {
	if !d.shared.Load() {
		if d.factory == nil {
			return nil, fmt.Errorf("%w: %s", ErrServiceNotExists, d.name)
		}
		s, err := d.factory(scope)
		if err != nil {
			return nil, err
		}
		if err := callInit(s); err != nil { // 瞬态也走 Init
			return nil, err
		}
		return s, nil
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
	if err := callInit(s); err != nil {
		return nil, err // Init 失败不缓存，允许重试
	}
	d.instance = s
	d.resolved.Store(true)
	return s, nil
}

func (d *Definition) resolveWithParams(scope *resolveScope, params ...any) (any, error) {
	if d.paramsFactory == nil {
		return nil, fmt.Errorf("%w: %s", ErrServiceNotExists, d.name)
	}
	s, err := d.paramsFactory(scope, params...)
	if err != nil {
		return nil, err
	}
	if err := callInit(s); err != nil {
		return nil, err
	}
	return s, nil
}

// callInit 若实例实现 Initializer 则调用 Init
func callInit(s any) error {
	if i, ok := s.(Initializer); ok {
		return i.Init()
	}
	return nil
}

// dispose 释放单例资源并重置状态，使容器 Close 后可被重新解析（类似 Spring refresh）。
// 调用方需保证不与并发 Get 同时进行（Close 是关闭流程，应先停止业务解析）。
func (d *Definition) dispose() error {
	if !d.shared.Load() || !d.resolved.Load() {
		return nil
	}
	var err error
	if c, ok := d.instance.(Disposable); ok {
		err = c.Dispose()
	}
	// 重置：先清实例引用，再清标志位（读者先查标志位，false 即不会读到 nil 实例）
	d.instance = nil
	d.resolved.Store(false)
	return err
}

func NewDefinition(name string, factory BuildHandler, shared bool) *Definition {
	d := &Definition{
		name:    name,
		factory: factory,
	}
	d.shared.Store(shared)
	d.lazy.Store(true) // 默认懒加载，对应 Spring lazy-init=true
	return d
}

// NewParamsDefinition 带参服务：每次调用都重新构造，不是单例
func NewParamsDefinition(name string, factory BuildWithHandler) *Definition {
	return &Definition{
		name:          name,
		paramsFactory: factory,
	}
}

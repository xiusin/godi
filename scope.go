// Copyright All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package godi

import (
	"sync"
)

// 作用域常量，对应 Spring ConfigurableBeanFactory.SCOPE_SINGLETON/SCOPE_PROTOTYPE
const (
	ScopeSingleton  = "singleton"
	ScopePrototype  = "prototype"
)

// Scope 作用域接口，对应 Spring org.springframework.beans.factory.config.Scope。
// 不同的作用域决定 bean 实例的存储与生命周期：
//   - singleton：全容器唯一，由注册表管理
//   - prototype：每次获取新建，容器不持有
//   - 自定义：如 request/session，由调用方实现 Get/Remove
type Scope interface {
	// Name 作用域名
	Name() string
	// Get 获取/创建实例。objectFactory 在缓存未命中时被调用以创建新实例
	// （对应 Spring Scope.get(name, objectFactory)）
	Get(name string, objectFactory func() (any, error)) (any, error)
	// Remove 移除作用域内实例（对应 Spring Scope.remove）
	Remove(name string)
}

// ---- singleton 作用域 ----
// 实际的实例存储委托给 singletonRegistry（三级缓存），这里只做转发，
// 保证 Scope 接口语义完整。singleton 的实例化协调由 BeanFactory 主流程负责。
type singletonScope struct{}

func (singletonScope) Name() string { return ScopeSingleton }

func (singletonScope) Get(name string, objectFactory func() (any, error)) (any, error) {
	// 真正的 singleton 获取走 BeanFactory.getSingleton，这里不应被直接调用
	return objectFactory()
}

func (singletonScope) Remove(name string) {}

// ---- prototype 作用域 ----
type prototypeScope struct{}

func (prototypeScope) Name() string { return ScopePrototype }

// prototype 每次都调用 objectFactory 新建，不缓存
func (prototypeScope) Get(_ string, objectFactory func() (any, error)) (any, error) {
	return objectFactory()
}

func (prototypeScope) Remove(_ string) {}

// scopeRegistry 管理作用域注册表
type scopeRegistry struct {
	mu     sync.RWMutex
	scopes map[string]Scope
}

func newScopeRegistry() *scopeRegistry {
	r := &scopeRegistry{scopes: make(map[string]Scope)}
	r.scopes[ScopeSingleton] = singletonScope{}
	r.scopes[ScopePrototype] = prototypeScope{}
	return r
}

func (r *scopeRegistry) get(name string) (Scope, bool) {
	r.mu.RLock()
	s, ok := r.scopes[name]
	r.mu.RUnlock()
	return s, ok
}

func (r *scopeRegistry) register(name string, s Scope) {
	r.mu.Lock()
	r.scopes[name] = s
	r.mu.Unlock()
}

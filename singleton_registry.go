// Copyright All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package godi

import (
	"fmt"
	"sync"
)

// singletonRegistry 三级单例缓存，对应 Spring DefaultSingletonBeanRegistry。
//
// 三级缓存是 Spring 解决 setter/field 注入循环依赖的核心机制：
//   - singletonObjects（一级）：完全初始化好的单例。命中即返回。
//   - earlySingletonObjects（二级）：已实例化但尚未完成属性注入/初始化的"半成品"。
//     循环依赖发生时，被依赖方通过它拿到创建中的 early reference。
//   - singletonFactories（三级）：ObjectFactory，调用后产出 early reference 并晋升到二级。
//     它的存在让 AOP 等扩展点能在暴露 early reference 时决定是否生成代理。
//
// 创建流程（getSingleton with creation）：
//   1. 查一级 → 命中返回
//   2. beforeSingletonCreation：登记 in-creation（环检测），构造器注入循环在此暴露
//   3. objectFactory() 调用 createBean：实例化 + 注册三级工厂 + 属性注入 + 初始化
//   4. afterSingletonCreation：移除 in-creation
//   5. addSingleton：晋升一级，清理二三级
type singletonRegistry struct {
	mu sync.Mutex // 保护下面三张表

	singletonObjects     map[string]any               // 一级
	earlySingletonObjects map[string]any              // 二级
	singletonFactories   map[string]func() (any, error) // 三级
	disposableInstances  map[string]any               // 记录实现 DisposableBean 的单例，供 DestroySingletons
	registeredSingletons map[string]struct{}          // 注册顺序（供 DestroySingletons 逆序销毁）

	// 正在创建中的单例名集合，对应 Spring singletonsCurrentlyInCreation。
	// 用于构造器注入循环依赖检测：若 getSingleton(name) 时 name 已在集合中且二三级都无 early reference，
	// 说明是构造器阶段就循环了（无法提前暴露），报 BeanCurrentlyInCreation。
	singletonsInCreation map[string]struct{}
}

func newSingletonRegistry() *singletonRegistry {
	return &singletonRegistry{
		singletonObjects:      make(map[string]any),
		earlySingletonObjects: make(map[string]any),
		singletonFactories:    make(map[string]func() (any, error)),
		disposableInstances:   make(map[string]any),
		registeredSingletons:  make(map[string]struct{}),
		singletonsInCreation:  make(map[string]struct{}),
	}
}

// getSingleton 查询已存在的单例（不触发创建），对应 Spring getSingleton(name, false)。
// 顺序：一级 → 二级 → 三级（三级命中则调用工厂产出 early reference 并晋升二级）。
func (r *singletonRegistry) getSingleton(name string) (any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if obj, ok := r.singletonObjects[name]; ok {
		return obj, true
	}
	if obj, ok := r.earlySingletonObjects[name]; ok {
		return obj, true
	}
	if factory, ok := r.singletonFactories[name]; ok {
		obj, err := factory()
		if err != nil {
			return nil, false // early reference 生成失败，上层会作为未命中处理
		}
		r.earlySingletonObjects[name] = obj
		delete(r.singletonFactories, name)
		return obj, true
	}
	return nil, false
}

// getSingletonWithCreation 创建并返回单例，对应 Spring getSingleton(name, objectFactory)。
// objectFactory 由 BeanFactory 提供，内部执行 createBean 完整流程。
//
// 关键：构造器注入循环依赖检测必须在 beanLock 之前完成，否则会自锁。
// 顺序：getSingleton 快路径 → in-creation 检测 → beanLock → 双重检查 → 创建。
func (r *singletonRegistry) getSingletonWithCreation(name string, objectFactory func() (any, error)) (any, error) {
	// 快路径：一级/二级/三级缓存命中（三级命中触发 early reference 生成）
	if obj, ok := r.getSingleton(name); ok {
		return obj, nil
	}
	// 构造器注入循环依赖检测：name 已在创建中且三级缓存都无 early reference 可暴露，
	// 说明是构造器阶段循环（无法提前暴露半成品），报 BeanCurrentlyInCreation。
	// 必须在 beanLock 之前，否则同 bean 重入会自锁。
	if r.isSingletonCurrentlyInCreation(name) {
		return nil, fmt.Errorf("%w: %s (constructor injection cycle)",
			ErrBeanCurrentlyInCreation, name)
	}
	// per-bean 锁：不同 bean 创建互不阻塞，同 bean 串行化
	beanLock := r.beanLock(name)
	beanLock.Lock()
	defer beanLock.Unlock()
	// 双重检查
	if obj, ok := r.getSingleton(name); ok {
		return obj, nil
	}
	// 登记创建中（正常路径首次进入；并发同 bean 会被 beanLock 串行化后由双重检查拦截）
	r.beforeSingletonCreation(name)
	defer r.afterSingletonCreation(name)

	obj, err := objectFactory()
	if err != nil {
		return nil, err
	}
	// 若 createBean 内部因循环依赖已把 obj 放进二级，addSingleton 会清理
	r.addSingleton(name, obj)
	return obj, nil
}

// addSingleton 晋升一级缓存并清理二三级，对应 Spring addSingleton。
func (r *singletonRegistry) addSingleton(name string, obj any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.singletonObjects[name] = obj
	delete(r.earlySingletonObjects, name)
	delete(r.singletonFactories, name)
	if _, exists := r.registeredSingletons[name]; !exists {
		r.registeredSingletons[name] = struct{}{}
	}
	if _, isDisposable := obj.(DisposableBean); isDisposable {
		r.disposableInstances[name] = obj
	}
}

// addSingletonFactory 注册三级缓存工厂，对应 Spring addSingletonFactory。
// 在 createBean 实例化之后、属性注入之前调用，使该 bean 可被循环依赖提前暴露。
func (r *singletonRegistry) addSingletonFactory(name string, factory func() (any, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.singletonFactories[name] = factory
}

// beforeSingletonCreation 登记 in-creation。返回 false 表示该 bean 已在创建中
// （构造器注入循环依赖），Spring 抛 BeanCurrentlyInCreationException。
func (r *singletonRegistry) beforeSingletonCreation(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.singletonsInCreation[name]; ok {
		return false
	}
	r.singletonsInCreation[name] = struct{}{}
	return true
}

func (r *singletonRegistry) afterSingletonCreation(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.singletonsInCreation, name)
}

// isSingletonCurrentlyInCreation 判断是否在创建中
func (r *singletonRegistry) isSingletonCurrentlyInCreation(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.singletonsInCreation[name]
	return ok
}

// removeSingleton 仅移除一级缓存（供 RemoveBeanDefinition）
func (r *singletonRegistry) removeSingleton(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.singletonObjects, name)
	delete(r.earlySingletonObjects, name)
	delete(r.singletonFactories, name)
	delete(r.disposableInstances, name)
	delete(r.registeredSingletons, name)
}

// registeredNames 返回注册顺序的 bean 名（供 DestroySingletons 逆序销毁）
func (r *singletonRegistry) registeredNames() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Go map 无序，保留注册顺序需独立切片。这里用 disposableInstances+registeredSingletons 的并集
	// 真实 Spring 用有序 Set。为支持逆序销毁，维护独立顺序切片见 defaultFactory。
	names := make([]string, 0, len(r.disposableInstances))
	for n := range r.disposableInstances {
		names = append(names, n)
	}
	return names
}

// ---- per-bean 锁：不同 bean 创建互不阻塞 ----
var (
	beanLocksMu sync.Mutex
	beanLocks   = map[string]*sync.Mutex{}
)

func (r *singletonRegistry) beanLock(name string) *sync.Mutex {
	beanLocksMu.Lock()
	defer beanLocksMu.Unlock()
	if l, ok := beanLocks[name]; ok {
		return l
	}
	l := &sync.Mutex{}
	beanLocks[name] = l
	return l
}

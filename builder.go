// Copyright All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package godi

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// 哨兵错误，调用方可通过 errors.Is 精确判别
var (
	ErrNotSupportedType   = errors.New("unsupported type")
	ErrServiceNotExists   = errors.New("service not exists")
	ErrServiceSingleton   = errors.New("service is singleton, cannot use it with GetWithParams")
	ErrCircularDependency = errors.New("circular dependency detected")
)

// AbstractBuilder DI 容器抽象接口
type AbstractBuilder interface {
	Bind(any, BuildHandler) *Definition
	Singleton(any, BuildHandler) *Definition
	Instance(any, ...any) *Definition
	Register(...AbstractServiceProvider)

	Set(any, BuildHandler, bool) *Definition
	SetWithParams(any, BuildWithHandler) *Definition
	Add(*Definition)
	Delete(any) bool
	Get(any) (any, error)
	GetWithParams(any, ...any) (any, error)
	MustGet(any, ...any) any
	GetDefinition(any) (*Definition, error)
	Exists(any) bool
	Names() []string
	Close() error
}

type BuildHandler     func(builder AbstractBuilder) (any, error)
type BuildWithHandler func(builder AbstractBuilder, params ...any) (any, error)

// builder 线程安全的 DI 容器实现。
//
// 用 map + RWMutex 替代 sync.Map：DI 容器注册后 key 基本稳定、读远多于写，
// 且需要点查/迭代/计数，map+RWMutex 在点查与迭代上均优于 sync.Map。
type builder struct {
	mu       sync.RWMutex
	services map[string]*Definition
}

// New 创建独立容器实例，支持多实例隔离与可测试性
func New() AbstractBuilder {
	return &builder{services: make(map[string]*Definition)}
}

func (b *builder) GetDefinition(serviceAny any) (*Definition, error) {
	name := ResolveServiceName(serviceAny)
	b.mu.RLock()
	def, ok := b.services[name]
	b.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrServiceNotExists, name)
	}
	return def, nil
}

func (b *builder) Instance(serviceAny any, instance ...any) *Definition {
	if len(instance) == 0 {
		if reflect.ValueOf(serviceAny).Kind() != reflect.Ptr {
			panic(ErrNotSupportedType)
		}
		instance = []any{serviceAny}
	}
	return b.Set(serviceAny, func(builder AbstractBuilder) (any, error) {
		return instance[0], nil
	}, true)
}

func (b *builder) Bind(serviceAny any, handler BuildHandler) *Definition {
	return b.Set(serviceAny, handler, false)
}

func (b *builder) Singleton(serviceAny any, handler BuildHandler) *Definition {
	return b.Set(serviceAny, handler, true)
}

func (b *builder) Set(serviceAny any, handler BuildHandler, singleton bool) *Definition {
	def := NewDefinition(ResolveServiceName(serviceAny), handler, singleton)
	b.mu.Lock()
	b.services[def.name] = def
	b.mu.Unlock()
	return def
}

func (b *builder) SetWithParams(serviceAny any, handler BuildWithHandler) *Definition {
	def := NewParamsDefinition(ResolveServiceName(serviceAny), handler)
	b.mu.Lock()
	b.services[def.name] = def
	b.mu.Unlock()
	return def
}

func (b *builder) Add(def *Definition) {
	b.mu.Lock()
	b.services[def.name] = def
	b.mu.Unlock()
}

func (b *builder) Delete(serviceAny any) bool {
	name := ResolveServiceName(serviceAny)
	b.mu.Lock()
	_, ok := b.services[name]
	delete(b.services, name)
	b.mu.Unlock()
	return ok
}

// Register 修复：使用 receiver b 而非全局 di，否则在非默认容器上注册会落到全局容器
func (b *builder) Register(providers ...AbstractServiceProvider) {
	for _, provider := range providers {
		provider.Register(b)
	}
}

func (b *builder) Get(serviceAny any) (any, error) {
	name := ResolveServiceName(serviceAny)
	b.mu.RLock()
	def, ok := b.services[name]
	b.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrServiceNotExists, name)
	}
	// 单例已解析：无锁快路径，跳过 scope 分配与循环依赖检测
	if def.shared.Load() && def.resolved.Load() {
		return def.instance, nil
	}
	// 冷路径：复用已查到的 def，避免 scope 内再次 RLock+map 查找
	s := &resolveScope{builder: b}
	s.enter(name)
	v, err := def.resolve(s)
	s.leave(name)
	return v, err
}

func (b *builder) GetWithParams(serviceAny any, params ...any) (any, error) {
	name := ResolveServiceName(serviceAny)
	b.mu.RLock()
	def, ok := b.services[name]
	b.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrServiceNotExists, name)
	}
	if def.paramsFactory == nil {
		return nil, fmt.Errorf("%w: %s", ErrServiceSingleton, name)
	}
	s := &resolveScope{builder: b}
	s.enter(name)
	v, err := def.resolveWithParams(s, params...)
	s.leave(name)
	return v, err
}

func (b *builder) MustGet(serviceAny any, params ...any) any {
	var (
		v   any
		err error
	)
	if len(params) == 0 {
		v, err = b.Get(serviceAny)
	} else {
		v, err = b.GetWithParams(serviceAny, params...)
	}
	if err != nil {
		panic(err)
	}
	return v
}

// Exists 修复：原实现用 sync.Map.Range 全表扫描 O(n)，改为点查 O(1)
func (b *builder) Exists(serviceAny any) bool {
	b.mu.RLock()
	_, ok := b.services[ResolveServiceName(serviceAny)]
	b.mu.RUnlock()
	return ok
}

func (b *builder) Names() []string {
	b.mu.RLock()
	names := make([]string, 0, len(b.services))
	for n := range b.services {
		names = append(names, n)
	}
	b.mu.RUnlock()
	return names
}

// Close 释放所有已构造单例持有的资源（实现 Disposable 的实例）。
// 修复：原实现持写锁期间调用用户 Dispose，若 Dispose 回调 Get 会死锁。
// 现改为 RLock 快照 → 释放锁 → 无锁回调，与 Spring destroySingletons 一致。
// 注意：Close 不可与并发 Get 同时进行（关闭流程应先停止业务解析）。
func (b *builder) Close() error {
	b.mu.RLock()
	defs := make([]*Definition, 0, len(b.services))
	for _, d := range b.services {
		defs = append(defs, d)
	}
	b.mu.RUnlock()
	var errs []error
	for _, d := range defs {
		if err := d.dispose(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ---- 服务名解析 ----

// nameCache 缓存 reflect.Type -> 服务名，避免每次 Get 都走反射
var nameCache sync.Map

func ResolveServiceName(service any) string {
	switch s := service.(type) {
	case string:
		return s
	case nil:
		panic("service name nil is not support")
	default:
		t := reflect.TypeOf(service)
		if v, ok := nameCache.Load(t); ok {
			return v.(string)
		}
		if t.Kind() != reflect.Ptr {
			panic(fmt.Errorf("service name type(%s) is not support", t.String()))
		}
		name := GetFullName(t)
		nameCache.Store(t, name)
		return name
	}
}

// GetFullName 去掉 fmt.Sprintf，用字符串拼接减少分配
func GetFullName(p reflect.Type) string {
	name := p.String()
	for p.Kind() == reflect.Ptr {
		p = p.Elem()
	}
	return p.PkgPath() + "@" + name
}

// ---- resolveScope：解析作用域，承载循环依赖检测 ----
//
// 工厂函数接收的 AbstractBuilder 即为该 scope，其 Get/GetWithParams/MustGet
// 会维护本条解析链的 visiting 栈，从而在加锁“之前”识别循环依赖，避免持锁
// 重入导致的死锁。其余方法（注册/查询元数据/Close 等）通过内嵌 *builder 透传
// 到容器本身。每次顶层 Get 创建独立 scope，并发不同解析链互不干扰（无假阳性）。
//
// visiting 用内联 [8]string 栈实现：深度 ≤8 时零堆分配（覆盖绝大多数依赖链），
// 仅在极深的异常链溢出时回退到 map。enter/leave 显式成对调用，避免 defer 在
// 热路径上的堆分配。
type resolveScope struct {
	*builder
	stack    [8]string          // 内联解析栈，LIFO
	sp       int                // 栈指针（已用槽位数）
	overflow map[string]struct{} // 深度>8 时溢出区，极少触发
}

// enter 压栈。深度 ≤8 用内联数组（零分配），超出则回退 map
func (s *resolveScope) enter(name string) {
	if s.sp < len(s.stack) {
		s.stack[s.sp] = name
		s.sp++
		return
	}
	if s.overflow == nil {
		s.overflow = make(map[string]struct{})
	}
	s.overflow[name] = struct{}{}
}

// leave 弹栈。LIFO：栈顶匹配则弹内联栈，否则在溢出 map 中删除
func (s *resolveScope) leave(name string) {
	if s.sp > 0 && s.stack[s.sp-1] == name {
		s.stack[s.sp-1] = "" // 助 GC
		s.sp--
		return
	}
	delete(s.overflow, name)
}

// cyclic 环检测：先扫内联栈，再查溢出 map
func (s *resolveScope) cyclic(name string) bool {
	for i := 0; i < s.sp; i++ {
		if s.stack[i] == name {
			return true
		}
	}
	if s.overflow != nil {
		_, ok := s.overflow[name]
		return ok
	}
	return false
}

func (s *resolveScope) Get(serviceAny any) (any, error) {
	name := ResolveServiceName(serviceAny)
	if s.cyclic(name) {
		return nil, fmt.Errorf("%w: %s", ErrCircularDependency, name)
	}
	s.builder.mu.RLock()
	def, ok := s.builder.services[name]
	s.builder.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrServiceNotExists, name)
	}
	if def.shared.Load() && def.resolved.Load() {
		return def.instance, nil
	}
	s.enter(name)
	v, err := def.resolve(s)
	s.leave(name)
	return v, err
}

func (s *resolveScope) GetWithParams(serviceAny any, params ...any) (any, error) {
	name := ResolveServiceName(serviceAny)
	if s.cyclic(name) {
		return nil, fmt.Errorf("%w: %s", ErrCircularDependency, name)
	}
	s.builder.mu.RLock()
	def, ok := s.builder.services[name]
	s.builder.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrServiceNotExists, name)
	}
	if def.paramsFactory == nil {
		return nil, fmt.Errorf("%w: %s", ErrServiceSingleton, name)
	}
	s.enter(name)
	v, err := def.resolveWithParams(s, params...)
	s.leave(name)
	return v, err
}

func (s *resolveScope) MustGet(serviceAny any, params ...any) any {
	var (
		v   any
		err error
	)
	if len(params) == 0 {
		v, err = s.Get(serviceAny)
	} else {
		v, err = s.GetWithParams(serviceAny, params...)
	}
	if err != nil {
		panic(err)
	}
	return v
}

// ---- 泛型 API（Go 1.18+），消除调用方类型断言 ----

// GetT 类型安全地获取服务。约定 T 为指针类型（与 Bind((*T)(nil)) 注册一致）
func GetT[T any](b AbstractBuilder) (T, error) {
	t := reflect.TypeOf((*T)(nil)).Elem()
	v, err := b.Get(reflect.Zero(t).Interface())
	if err != nil {
		var zero T
		return zero, err
	}
	return v.(T), nil
}

// MustGetT 类型安全地获取服务，失败 panic
func MustGetT[T any](b AbstractBuilder) T {
	v, err := GetT[T](b)
	if err != nil {
		panic(err)
	}
	return v
}

// ---- 全局默认容器（包级 API 兼容）----

var di = &builder{services: make(map[string]*Definition)}

func GetDefaultDI() AbstractBuilder { return di }

func Get(serviceAny any) (any, error)           { return di.Get(serviceAny) }
func MustGet(serviceAny any, params ...any) any { return di.MustGet(serviceAny, params...) }
func Exists(serviceAny any) bool                { return di.Exists(serviceAny) }
func Remove(serviceAny any)                     { _ = di.Delete(serviceAny) }

func Bind(serviceAny any, handler BuildHandler) *Definition {
	return di.Bind(serviceAny, handler)
}

func Bound(serviceAny any) bool { return Exists(serviceAny) }

// IsShare 修复：原实现 MustGet(...).(*Definition) 类型断言必然 panic；
// 改为直接读取元数据，且不再因查询而触发构造
func IsShare(serviceAny any) bool {
	def, err := di.GetDefinition(serviceAny)
	if err != nil {
		return false
	}
	return def.IsSingleton()
}

func Set(serviceAny any, handler BuildHandler, singleton bool) *Definition {
	return di.Set(serviceAny, handler, singleton)
}

func Attempt(serviceAny any, handler BuildHandler, singleton bool) *Definition {
	if Bound(serviceAny) {
		return nil
	}
	return Set(serviceAny, handler, singleton)
}

func Instance(serviceAny any, instance ...any) *Definition {
	return di.Instance(serviceAny, instance...)
}

func SetWithParams(serviceAny any, handler BuildWithHandler) *Definition {
	return di.SetWithParams(serviceAny, handler)
}

func GetWithParams(serviceName string, params ...any) (any, error) {
	return di.GetWithParams(serviceName, params...)
}

func Register(providers ...AbstractServiceProvider) {
	di.Register(providers...)
}

// InjectOn 解析 object 内可识别的 nil 指针字段并自动注入。
// 仅注入可导出且当前为 nil 的指针字段；引用服务非数据安全，需自行管理。
// 修复：原条件 value.Kind()!=Ptr && value.Elem() 在非指针时直接 panic；
// 且 field.Set 对未导出字段会 panic，这里统一跳过。
func InjectOn(ptr any) {
	v := reflect.ValueOf(ptr)
	if v.Kind() != reflect.Ptr || v.Elem().Kind() != reflect.Struct {
		panic(ErrNotSupportedType)
	}
	e := v.Elem()
	for i := 0; i < e.NumField(); i++ {
		f := e.Field(i)
		if f.Kind() != reflect.Ptr || !f.IsNil() {
			continue
		}
		if !f.CanSet() {
			continue
		}
		if svc, err := di.Get(f.Interface()); err == nil {
			f.Set(reflect.ValueOf(svc))
		}
	}
}

func List() []string {
	return di.Names()
}

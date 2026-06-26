// Copyright All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package godi

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// 哨兵错误，调用方可通过 errors.Is 精确判别
var (
	ErrNotSupportedType   = errors.New("unsupported type")
	ErrServiceNotExists   = errors.New("service not exists")
	ErrServiceSingleton   = errors.New("service is singleton, cannot use it with GetWithParams")
	ErrCircularDependency = errors.New("circular dependency detected")
	ErrInitFailed         = errors.New("initializer failed")
)

// AbstractBuilder DI 容器抽象接口（对标 Spring BeanFactory / HierarchicalBeanFactory）
type AbstractBuilder interface {
	// 注册
	Bind(any, BuildHandler) *Definition
	Singleton(any, BuildHandler) *Definition
	Instance(any, ...any) *Definition
	Set(any, BuildHandler, bool) *Definition
	SetWithParams(any, BuildWithHandler) *Definition
	Add(*Definition)
	Alias(alias, target any) *Definition // 注册别名指向目标服务（Spring alias）
	Delete(any) bool
	Register(...AbstractServiceProvider)

	// 获取（找不到时委托父容器，对应 Spring HierarchicalBeanFactory）
	Get(any) (any, error)
	GetWithParams(any, ...any) (any, error)
	MustGet(any, ...any) any
	GetDefinition(any) (*Definition, error)
	Exists(any) bool
	Names() []string

	// 生命周期（对应 Spring lazy-init 预实例化 + destroySingletons 逆序销毁）
	PreWarm() error // 预热所有 lazy=false 的单例
	Close() error

	// 层级
	Parent() AbstractBuilder
}

type BuildHandler     func(builder AbstractBuilder) (any, error)
type BuildWithHandler func(builder AbstractBuilder, params ...any) (any, error)

// builder 线程安全的 DI 容器实现。
//
// 用 map + RWMutex 替代 sync.Map：注册后 key 稳定、读远多于写，点查/迭代更优。
// deps 记录单例间依赖关系（A 依赖 B），供 Close 按依赖逆序销毁（Spring destroySingletons）。
type builder struct {
	mu       sync.RWMutex
	services map[string]*Definition
	parent   AbstractBuilder

	// 依赖图：deps[A] = {B,C} 表示 A 构造期间依赖 B、C。
	// 仅在单例首次构造的冷路径记录，热路径不触碰；供 Close 拓扑逆序销毁。
	depsMu sync.Mutex
	deps   map[string]map[string]struct{}
}

// New 创建独立容器实例，支持多实例隔离与可测试性。
// 可选传入父容器，子容器找不到服务时委托父容器查找（对应 Spring 父子容器）。
func New(parents ...AbstractBuilder) AbstractBuilder {
	b := &builder{
		services: make(map[string]*Definition),
		deps:     make(map[string]map[string]struct{}),
	}
	if len(parents) > 0 {
		b.parent = parents[0]
	}
	return b
}

func (b *builder) Parent() AbstractBuilder { return b.parent }

func (b *builder) GetDefinition(serviceAny any) (*Definition, error) {
	name := ResolveServiceName(serviceAny)
	b.mu.RLock()
	def, ok := b.services[name]
	b.mu.RUnlock()
	if !ok {
		// 委托父容器
		if b.parent != nil {
			return b.parent.GetDefinition(serviceAny)
		}
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

// Alias 为已注册的目标服务注册一个别名。别名与主名指向同一 Definition，
// 后续 Get(别名) 等价于 Get(目标)。对应 Spring <bean name="x" alias="y"/>。
func (b *builder) Alias(alias, target any) *Definition {
	aliasName := ResolveServiceName(alias)
	b.mu.RLock()
	def, ok := b.services[ResolveServiceName(target)]
	b.mu.RUnlock()
	if !ok {
		panic(fmt.Errorf("%w: alias target not registered", ErrServiceNotExists))
	}
	b.mu.Lock()
	b.services[aliasName] = def
	b.mu.Unlock()
	return def
}

func (b *builder) Delete(serviceAny any) bool {
	name := ResolveServiceName(serviceAny)
	b.mu.Lock()
	_, ok := b.services[name]
	delete(b.services, name)
	b.mu.Unlock()
	return ok
}

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
		if b.parent != nil {
			return b.parent.Get(serviceAny)
		}
		return nil, fmt.Errorf("%w: %s", ErrServiceNotExists, name)
	}
	// 单例已解析：无锁快路径
	if def.shared.Load() && def.resolved.Load() {
		return def.instance, nil
	}
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
		if b.parent != nil {
			return b.parent.GetWithParams(serviceAny, params...)
		}
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

// Exists 本容器或父容器是否注册了该服务
func (b *builder) Exists(serviceAny any) bool {
	b.mu.RLock()
	_, ok := b.services[ResolveServiceName(serviceAny)]
	b.mu.RUnlock()
	if ok {
		return true
	}
	if b.parent != nil {
		return b.parent.Exists(serviceAny)
	}
	return false
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

// PreWarm 预热所有 lazy=false 的单例（对应 Spring refresh 阶段的非懒加载预实例化）。
// 已解析的跳过；构造失败的返回错误。仅预热本容器，不含父容器。
func (b *builder) PreWarm() error {
	b.mu.RLock()
	defs := make([]*Definition, 0, len(b.services))
	for _, d := range b.services {
		if d.shared.Load() && !d.lazy.Load() && !d.resolved.Load() {
			defs = append(defs, d)
		}
	}
	b.mu.RUnlock()
	for _, d := range defs {
		if _, err := b.Get(d.name); err != nil {
			return err
		}
	}
	return nil
}

// Close 释放所有已构造单例持有的资源，按依赖逆序销毁（先销毁依赖者，再销毁被依赖者），
// 对应 Spring destroySingletons 的有序销毁。RLock 快照 → 释放锁 → 无锁回调，避免
// Dispose 回调 Get 造成死锁。Close 不可与并发 Get 同时进行。
func (b *builder) Close() error {
	b.mu.RLock()
	defs := make([]*Definition, 0, len(b.services))
	seen := make(map[*Definition]struct{}, len(b.services))
	for _, d := range b.services {
		if _, ok := seen[d]; ok {
			continue // 别名去重：同一 Definition 只销毁一次
		}
		seen[d] = struct{}{}
		defs = append(defs, d)
	}
	b.mu.RUnlock()

	// 仅保留已解析的单例
	resolved := make([]*Definition, 0, len(defs))
	for _, d := range defs {
		if d.shared.Load() && d.resolved.Load() {
			resolved = append(resolved, d)
		}
	}
	// 按依赖逆序销毁：依赖者先 dispose
	ordered := b.topoOrder(resolved)
	var errs []error
	for _, d := range ordered {
		if err := d.dispose(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// topoOrder 对已解析单例做拓扑排序，返回"依赖者优先"的销毁顺序。
// 边 A→B 表示 A 依赖 B；销毁要 A 先于 B，故输出拓扑正序（入度0即纯依赖者在前）。
func (b *builder) topoOrder(defs []*Definition) []*Definition {
	if len(defs) <= 1 {
		return defs
	}
	set := make(map[string]*Definition, len(defs))
	for _, d := range defs {
		set[d.name] = d
	}
	// 入度 = 该节点被多少个同集合内节点依赖
	indeg := make(map[string]int, len(defs))
	adj := make(map[string][]string, len(defs)) // A -> [依赖的B...]
	b.depsMu.Lock()
	for _, d := range defs {
		adj[d.name] = nil
		indeg[d.name] = 0
	}
	for from, tos := range b.deps {
		if _, ok := set[from]; !ok {
			continue
		}
		for to := range tos {
			if _, ok := set[to]; !ok {
				continue
			}
			adj[from] = append(adj[from], to) // from 依赖 to
			indeg[to]++                       // to 被依赖，入度+1
		}
	}
	b.depsMu.Unlock()

	// Kahn：入度0（纯依赖者，无人依赖它）先出队 → 销毁顺序
	queue := make([]string, 0, len(defs))
	for n, d := range indeg {
		if d == 0 {
			queue = append(queue, n)
		}
	}
	out := make([]*Definition, 0, len(defs))
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		out = append(out, set[n])
		for _, to := range adj[n] {
			indeg[to]--
			if indeg[to] == 0 {
				queue = append(queue, to)
			}
		}
	}
	// 剩余（理论不存在，因环已被检测拒绝）兜底追加
	if len(out) < len(defs) {
		appended := make(map[string]struct{}, len(out))
		for _, d := range out {
			appended[d.name] = struct{}{}
		}
		for _, d := range defs {
			if _, ok := appended[d.name]; !ok {
				out = append(out, d)
			}
		}
	}
	return out
}

// recordDep 记录依赖边 from→to（from 构造期间依赖 to），用于 Close 逆序销毁。
// 仅冷路径调用，depsMu 保护。
func (b *builder) recordDep(from, to string) {
	b.depsMu.Lock()
	if b.deps[from] == nil {
		b.deps[from] = make(map[string]struct{})
	}
	b.deps[from][to] = struct{}{}
	b.depsMu.Unlock()
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

// ---- resolveScope：解析作用域，承载循环依赖检测与依赖图记录 ----
//
// visiting 用内联 [8]string 栈实现：深度 ≤8 时零堆分配，溢出回退 map。
// enter/leave 显式成对调用，避免 defer 在热路径上的堆分配。
// 检测到环时，从栈中拼出完整依赖链 A → B → A，便于排查（对应 Spring 的
// BeanCurrentlyInCreationException 带路径）。
type resolveScope struct {
	*builder
	stack    [8]string
	sp       int
	overflow map[string]struct{}
}

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

func (s *resolveScope) leave(name string) {
	if s.sp > 0 && s.stack[s.sp-1] == name {
		s.stack[s.sp-1] = "" // 助 GC
		s.sp--
		return
	}
	delete(s.overflow, name)
}

// cyclic 返回环路径。无环返回空串；有环返回 "A -> B -> A" 形式。
func (s *resolveScope) cyclic(name string) string {
	// 先在内联栈中找环起点
	for i := 0; i < s.sp; i++ {
		if s.stack[i] == name {
			chain := make([]string, 0, s.sp-i+1)
			chain = append(chain, s.stack[i:s.sp]...)
			chain = append(chain, name)
			return strings.Join(chain, " -> ")
		}
	}
	if s.overflow != nil {
		if _, ok := s.overflow[name]; ok {
			// 溢出区发生环，退化为仅报告环点
			return name + " -> " + name + " (deep chain)"
		}
	}
	return ""
}

// pathSnapshot 返回当前解析栈快照，用于错误诊断
func (s *resolveScope) pathSnapshot() []string {
	snap := make([]string, 0, s.sp+len(s.overflow))
	snap = append(snap, s.stack[:s.sp]...)
	return snap
}

func (s *resolveScope) Get(serviceAny any) (any, error) {
	name := ResolveServiceName(serviceAny)
	if chain := s.cyclic(name); chain != "" {
		return nil, fmt.Errorf("%w: %s", ErrCircularDependency, chain)
	}
	// 依赖图记录：栈顶是当前正在构造的服务，它依赖 name
	if s.sp > 0 {
		s.builder.recordDep(s.stack[s.sp-1], name)
	}
	s.builder.mu.RLock()
	def, ok := s.builder.services[name]
	s.builder.mu.RUnlock()
	if !ok {
		if s.builder.parent != nil {
			return s.builder.parent.Get(serviceAny)
		}
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
	if chain := s.cyclic(name); chain != "" {
		return nil, fmt.Errorf("%w: %s", ErrCircularDependency, chain)
	}
	if s.sp > 0 {
		s.builder.recordDep(s.stack[s.sp-1], name)
	}
	s.builder.mu.RLock()
	def, ok := s.builder.services[name]
	s.builder.mu.RUnlock()
	if !ok {
		if s.builder.parent != nil {
			return s.builder.parent.GetWithParams(serviceAny, params...)
		}
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

var di = &builder{services: make(map[string]*Definition), deps: make(map[string]map[string]struct{})}

func GetDefaultDI() AbstractBuilder { return di }

func Get(serviceAny any) (any, error)           { return di.Get(serviceAny) }
func MustGet(serviceAny any, params ...any) any { return di.MustGet(serviceAny, params...) }
func Exists(serviceAny any) bool                { return di.Exists(serviceAny) }
func Remove(serviceAny any)                     { _ = di.Delete(serviceAny) }

func Bind(serviceAny any, handler BuildHandler) *Definition {
	return di.Bind(serviceAny, handler)
}

func Bound(serviceAny any) bool { return Exists(serviceAny) }

// IsShare 直接读取元数据，不再因查询而触发构造
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

// PreWarm 预热全局容器
func PreWarm() error { return di.PreWarm() }

// Alias 为全局容器注册别名
func Alias(alias, target any) *Definition { return di.Alias(alias, target) }

// InjectOn 解析 object 内可识别的 nil 指针字段并自动注入。
// 仅注入可导出且当前为 nil 的指针字段；引用服务非数据安全，需自行管理。
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

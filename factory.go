// Copyright All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package godi

import (
	"fmt"
	"reflect"
	"sort"
	"sync"
)

// DefaultBeanFactory Spring 风格的 BeanFactory 默认实现，对应 Spring DefaultListableBeanFactory。
// 完整复刻 createBean → populateBean → initializeBean 三阶段流程，并集成三级缓存解决
// setter/field 注入的循环依赖。
type DefaultBeanFactory struct {
	mu              sync.RWMutex
	beanDefinitions map[string]*BeanDefinition
	aliases         map[string]string // alias -> canonical name

	registry *singletonRegistry
	scopes   *scopeRegistry

	parent BeanFactory

	postProcessorsMu sync.RWMutex
	postProcessors   []BeanPostProcessor

	// 依赖图：from 依赖 to，供 DestroySingletons 拓扑逆序销毁
	depsMu sync.Mutex
	deps   map[string]map[string]struct{}

	// 单例注册顺序，供 DestroySingletons 逆序销毁（Spring 用 LinkedHashSet）
	creationOrderMu sync.Mutex
	creationOrder   []string
}

// NewDefaultBeanFactory 创建 BeanFactory，可选父容器
func NewDefaultBeanFactory(parent ...BeanFactory) *DefaultBeanFactory {
	f := &DefaultBeanFactory{
		beanDefinitions: make(map[string]*BeanDefinition),
		aliases:         make(map[string]string),
		registry:        newSingletonRegistry(),
		scopes:          newScopeRegistry(),
		deps:            make(map[string]map[string]struct{}),
	}
	if len(parent) > 0 {
		f.parent = parent[0]
	}
	return f
}

// ---- 注册 ----

func (f *DefaultBeanFactory) RegisterBeanDefinition(name string, bd *BeanDefinition) {
	if bd == nil {
		panic(ErrBeanDefinitionStore)
	}
	bd.applyDefaults()
	if bd.Name == "" {
		bd.Name = name
	}
	f.mu.Lock()
	f.beanDefinitions[name] = bd
	f.mu.Unlock()
}

func (f *DefaultBeanFactory) RegisterAlias(alias, name string) {
	f.mu.Lock()
	f.aliases[alias] = name
	f.mu.Unlock()
}

func (f *DefaultBeanFactory) canonicalName(name string) string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for {
		if canon, ok := f.aliases[name]; ok {
			name = canon
			continue
		}
		return name
	}
}

func (f *DefaultBeanFactory) RemoveBeanDefinition(name string) bool {
	f.mu.Lock()
	_, ok := f.beanDefinitions[name]
	delete(f.beanDefinitions, name)
	f.mu.Unlock()
	if ok {
		f.registry.removeSingleton(name)
	}
	return ok
}

func (f *DefaultBeanFactory) GetBeanDefinition(name string) (*BeanDefinition, error) {
	name = f.canonicalName(name)
	f.mu.RLock()
	bd, ok := f.beanDefinitions[name]
	f.mu.RUnlock()
	if !ok {
		if f.parent != nil {
			return f.parent.GetBeanDefinition(name)
		}
		return nil, fmt.Errorf("%w: %s", ErrNoSuchBeanDefinition, name)
	}
	return bd, nil
}

func (f *DefaultBeanFactory) ContainsBean(name string) bool {
	name = f.canonicalName(name)
	f.mu.RLock()
	_, ok := f.beanDefinitions[name]
	f.mu.RUnlock()
	if ok {
		return true
	}
	if f.parent != nil {
		return f.parent.ContainsBean(name)
	}
	return false
}

func (f *DefaultBeanFactory) GetParentBeanFactory() BeanFactory { return f.parent }

func (f *DefaultBeanFactory) RegisterScope(name string, scope Scope) {
	f.scopes.register(name, scope)
}

func (f *DefaultBeanFactory) GetScope(name string) (Scope, bool) { return f.scopes.get(name) }

func (f *DefaultBeanFactory) GetAliases(name string) []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]string, 0)
	for alias, canon := range f.aliases {
		if canon == name {
			out = append(out, alias)
		}
	}
	return out
}

// ---- PostProcessor ----

func (f *DefaultBeanFactory) AddBeanPostProcessor(p BeanPostProcessor) {
	f.postProcessorsMu.Lock()
	f.postProcessors = append(f.postProcessors, p)
	// 按 Order 升序排序
	sort.SliceStable(f.postProcessors, func(i, j int) bool {
		return orderOf(f.postProcessors[i]) < orderOf(f.postProcessors[j])
	})
	f.postProcessorsMu.Unlock()
}

func (f *DefaultBeanFactory) GetBeanPostProcessors() []BeanPostProcessor {
	f.postProcessorsMu.RLock()
	defer f.postProcessorsMu.RUnlock()
	out := make([]BeanPostProcessor, len(f.postProcessors))
	copy(out, f.postProcessors)
	return out
}

// ---- 获取 bean ----

func (f *DefaultBeanFactory) GetBean(name string) (any, error) {
	name = f.canonicalName(name)
	// 先查单例缓存（含二三级 early reference）
	if obj, ok := f.registry.getSingleton(name); ok {
		return obj, nil
	}
	bd, err := f.GetBeanDefinition(name)
	if err != nil {
		return nil, err
	}
	return f.doGetBean(name, bd)
}

func (f *DefaultBeanFactory) doGetBean(name string, bd *BeanDefinition) (any, error) {
	scope, ok := f.scopes.get(bd.Scope)
	if !ok {
		return nil, fmt.Errorf("%w: unknown scope %s", ErrBeanCreation, bd.Scope)
	}

	// depends-on：先确保依赖 bean 已创建
	for _, dep := range bd.DependsOn {
		if _, err := f.getBeanByName(dep); err != nil {
			return nil, fmt.Errorf("%w: depends-on '%s' for '%s': %v",
				ErrUnsatisfiedDependency, dep, name, err)
		}
	}

	switch scope.Name() {
	case ScopeSingleton:
		// 通过三级缓存注册表创建，内部 per-bean 锁 + in-creation 检测
		return f.registry.getSingletonWithCreation(name, func() (any, error) {
			return f.createBean(name, bd)
		})
	case ScopePrototype:
		// prototype 不进缓存，每次新建，但同样要检测循环
		if f.registry.isSingletonCurrentlyInCreation(name) {
			return nil, fmt.Errorf("%w: %s (prototype in creation cycle)",
				ErrBeanCurrentlyInCreation, name)
		}
		return f.createBean(name, bd)
	default:
		// 自定义 scope
		return scope.Get(name, func() (any, error) { return f.createBean(name, bd) })
	}
}

func (f *DefaultBeanFactory) getBeanByName(name string) (any, error) {
	name = f.canonicalName(name)
	if obj, ok := f.registry.getSingleton(name); ok {
		return obj, nil
	}
	bd, err := f.GetBeanDefinition(name)
	if err != nil {
		return nil, err
	}
	return f.doGetBean(name, bd)
}

// createBean 三阶段创建：实例化 → 属性注入 → 初始化。对应 Spring AbstractAutowireCapableBeanFactory.createBean。
// 关键点：实例化后立即注册三级缓存工厂，使 setter/field 注入的循环依赖能拿到 early reference。
func (f *DefaultBeanFactory) createBean(name string, bd *BeanDefinition) (any, error) {
	bean, err := f.instantiate(bd)
	if err != nil {
		return nil, fmt.Errorf("%w: instantiate '%s': %v", ErrBeanCreation, name, err)
	}

	// singleton：注册三级缓存，暴露 early reference（AOP 代理在此决定）
	if bd.IsSingleton() {
		f.registry.addSingletonFactory(name, func() (any, error) {
			// getEarlyBeanReference：当前直接返回原实例；BeanPostProcessor 可扩展为生成代理
			return f.getEarlyBeanReference(name, bean), nil
		})
	}

	// 属性注入（可能递归 getBean，触发循环依赖时通过三级缓存拿到 early reference）
	if err := f.populateBean(name, bd, bean); err != nil {
		return nil, fmt.Errorf("%w: populate '%s': %v", ErrBeanCreation, name, err)
	}

	// 初始化（PostProcessor.before → init → PostProcessor.after）
	initialized, err := f.initializeBean(name, bean, bd)
	if err != nil {
		return nil, fmt.Errorf("%w: initialize '%s': %v", ErrBeanCreation, name, err)
	}

	// 记录创建顺序与依赖图，供 DestroySingletons
	if bd.IsSingleton() {
		f.recordCreation(name)
	}
	return initialized, nil
}

// instantiate 实例化：优先 Factory/Constructor，否则反射 Type 构造
func (f *DefaultBeanFactory) instantiate(bd *BeanDefinition) (any, error) {
	if bd.Constructor != nil {
		return bd.Constructor(f)
	}
	if bd.Factory != nil {
		return bd.Factory(f)
	}
	if bd.Type != nil && bd.Type.Kind() == reflect.Ptr {
		// 反射 New 指针类型：如 *Foo → new(Foo)
		elem := bd.Type.Elem()
		if elem.Kind() == reflect.Struct {
			return reflect.New(elem).Interface(), nil
		}
	}
	if bd.Type != nil && bd.Type.Kind() == reflect.Struct {
		return reflect.New(bd.Type).Interface(), nil // 返回指针便于注入
	}
	return nil, fmt.Errorf("%w: no factory/constructor/type for %s", ErrBeanCreation, bd.Name)
}

// populateBean 属性注入，对应 Spring populateBean。
// 1) 显式 PropertyValues（Ref 优先于 Value）
// 2) Autowire 模式（byName/byType）扫描字段自动注入
func (f *DefaultBeanFactory) populateBean(name string, bd *BeanDefinition, bean any) error {
	v := reflect.ValueOf(bean)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil // 非结构体跳过字段注入
	}

	// 显式属性
	for _, pv := range bd.PropertyValues {
		field := v.FieldByName(pv.Name)
		if !field.IsValid() {
			return fmt.Errorf("%w: field '%s' not found in %s", ErrUnsatisfiedDependency, pv.Name, name)
		}
		if !field.CanSet() {
			return fmt.Errorf("%w: field '%s' unexported in %s", ErrUnsatisfiedDependency, pv.Name, name)
		}
		var val any
		if pv.Ref != "" {
			dep, err := f.getBeanByName(pv.Ref)
			if err != nil {
				return fmt.Errorf("%w: ref '%s' for %s.%s: %v", ErrUnsatisfiedDependency, pv.Ref, name, pv.Name, err)
			}
			val = dep
			f.recordDep(name, pv.Ref)
		} else {
			val = pv.Value
		}
		if err := setField(field, val); err != nil {
			return fmt.Errorf("%w: set %s.%s: %v", ErrUnsatisfiedDependency, name, pv.Name, err)
		}
	}

	// 自动装配
	if bd.Autowire != AutowireNo {
		if err := f.autowireFields(name, v, bd.Autowire); err != nil {
			return err
		}
	}
	return nil
}

// autowireFields 按模式扫描导出字段自动注入
func (f *DefaultBeanFactory) autowireFields(owner string, v reflect.Value, mode AutowireMode) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		fv := v.Field(i)
		if !fv.CanSet() {
			continue
		}
		// 仅注入当前为 nil 的指针/接口字段，避免覆盖已赋值
		if fv.Kind() == reflect.Ptr && !fv.IsNil() {
			continue
		}
		if !autowireCandidate(field.Type) {
			continue
		}
		var dep any
		var depName string
		var err error
		switch mode {
		case AutowireByType:
			dep, err = f.ResolveDependency(field.Type, owner)
			if err != nil {
				if err == errNoCandidate { // 无候选视为该字段无需注入
					continue
				}
				return err
			}
			depName = f.nameOfResolved(field.Type)
		case AutowireByName:
			depName = field.Name
			if !f.ContainsBean(depName) {
				continue
			}
			dep, err = f.getBeanByName(depName)
			if err != nil {
				return err
			}
		}
		if dep == nil {
			continue
		}
		f.recordDep(owner, depName)
		if err := setField(fv, dep); err != nil {
			return fmt.Errorf("%w: autowire %s.%s: %v", ErrUnsatisfiedDependency, owner, field.Name, err)
		}
	}
	return nil
}

// initializeBean 初始化三步，对应 Spring initializeBean。
func (f *DefaultBeanFactory) initializeBean(name string, bean any, bd *BeanDefinition) (any, error) {
	var err error
	// 1. before
	if bean, err = f.applyPostProcessorsBefore(bean, name); err != nil {
		return nil, err
	}
	// 2. init: InitializingBean + initMethod
	if err = f.invokeInitMethods(bean, bd); err != nil {
		return nil, err
	}
	// 3. after（AOP 代理通常在此生成；注意 early reference 已在循环依赖时暴露，可能与此不一致）
	if bean, err = f.applyPostProcessorsAfter(bean, name); err != nil {
		return nil, err
	}
	return bean, nil
}

func (f *DefaultBeanFactory) invokeInitMethods(bean any, bd *BeanDefinition) error {
	if ib, ok := bean.(InitializingBean); ok {
		if err := ib.AfterPropertiesSet(); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInitFailed, bd.Name, err)
		}
	}
	if bd.InitMethod != "" {
		v := reflect.ValueOf(bean)
		m := v.MethodByName(bd.InitMethod)
		if !m.IsValid() {
			return fmt.Errorf("%w: init-method '%s' not found on %s", ErrInitFailed, bd.InitMethod, bd.Name)
		}
		out := m.Call(nil)
		if len(out) == 1 {
			if err, ok := out[0].Interface().(error); ok && err != nil {
				return fmt.Errorf("%w: %s: %v", ErrInitFailed, bd.Name, err)
			}
		}
	}
	return nil
}

// getEarlyBeanReference 提前暴露的引用。PostProcessor 可在此返回代理（AOP）。
// 当前实现直接返回原 bean。
func (f *DefaultBeanFactory) getEarlyBeanReference(name string, bean any) any {
	for _, p := range f.GetBeanPostProcessors() {
		// 这里可定义 EarlyBeanReferenceHandler 扩展点，当前简化为直接返回
		_ = p
	}
	return bean
}

func (f *DefaultBeanFactory) applyPostProcessorsBefore(bean any, name string) (any, error) {
	for _, p := range f.GetBeanPostProcessors() {
		b, err := p.PostProcessBeforeInitialization(bean, name)
		if err != nil {
			return nil, err
		}
		if b != nil {
			bean = b
		}
	}
	return bean, nil
}

func (f *DefaultBeanFactory) applyPostProcessorsAfter(bean any, name string) (any, error) {
	for _, p := range f.GetBeanPostProcessors() {
		b, err := p.PostProcessAfterInitialization(bean, name)
		if err != nil {
			return nil, err
		}
		if b != nil {
			bean = b
		}
	}
	return bean, nil
}

// ---- byType ----

var errNoCandidate = fmt.Errorf("no candidate")

// ResolveDependency 按 type 解析唯一候选 bean（含父容器），对应 Spring resolveDependency。
func (f *DefaultBeanFactory) ResolveDependency(t reflect.Type, requestingBeanName string) (any, error) {
	names := f.GetBeanNamesForTypeAll(t)
	if len(names) == 0 {
		return nil, errNoCandidate
	}
	// 多候选：优先 Primary
	if len(names) > 1 {
		primaries := f.primaryNames(names)
		if len(primaries) == 1 {
			return f.getBeanByName(primaries[0])
		}
		if len(primaries) > 1 {
			return nil, fmt.Errorf("%w: multiple @Primary for type %s", ErrNoUniqueBean, t)
		}
		return nil, fmt.Errorf("%w: %d candidates for type %s", ErrNoUniqueBean, len(names), t)
	}
	return f.getBeanByName(names[0])
}

func (f *DefaultBeanFactory) primaryNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		bd, err := f.GetBeanDefinition(n)
		if err == nil && bd.Primary {
			out = append(out, n)
		}
	}
	return out
}

func (f *DefaultBeanFactory) nameOfResolved(t reflect.Type) string {
	names := f.GetBeanNamesForType(t)
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

// GetBeanNamesForType 本容器内匹配 type 的 bean 名
func (f *DefaultBeanFactory) GetBeanNamesForType(t reflect.Type) []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]string, 0)
	for name, bd := range f.beanDefinitions {
		if bd.Type != nil && bd.Type == t {
			out = append(out, name)
		}
	}
	return out
}

// GetBeanNamesForTypeAll 含父容器
func (f *DefaultBeanFactory) GetBeanNamesForTypeAll(t reflect.Type) []string {
	out := f.GetBeanNamesForType(t)
	if f.parent != nil {
		out = append(out, f.parent.GetBeanNamesForTypeAll(t)...)
	}
	return out
}

// GetBeanByType 按类型获取唯一 bean，含父容器
func (f *DefaultBeanFactory) GetBeanByType(t reflect.Type) (any, error) {
	return f.ResolveDependency(t, "")
}

// GetBeansOfType 获取某类型的所有 bean（name → instance）
func (f *DefaultBeanFactory) GetBeansOfType(t reflect.Type) (map[string]any, error) {
	names := f.GetBeanNamesForTypeAll(t)
	out := make(map[string]any, len(names))
	for _, n := range names {
		v, err := f.getBeanByName(n)
		if err != nil {
			return nil, err
		}
		out[n] = v
	}
	return out, nil
}

// ---- 生命周期 ----

// PreInstantiateSingletons 对应 Spring refresh 的 finishBeanFactoryInitialization。
// 预实例化所有 lazy=false 的单例。
func (f *DefaultBeanFactory) PreInstantiateSingletons() error {
	f.mu.RLock()
	names := make([]string, 0, len(f.beanDefinitions))
	for name, bd := range f.beanDefinitions {
		if bd.IsSingleton() && !bd.LazyInit {
			names = append(names, name)
		}
	}
	f.mu.RUnlock()
	// 按 Order 排序，保证预实例化顺序确定
	sort.SliceStable(names, func(i, j int) bool {
		bi, _ := f.GetBeanDefinition(names[i])
		bj, _ := f.GetBeanDefinition(names[j])
		return bi.Order < bj.Order
	})
	for _, n := range names {
		if _, err := f.GetBean(n); err != nil {
			return err
		}
	}
	return nil
}

// DestroySingletons 对应 Spring destroySingletons。按依赖逆序销毁单例。
func (f *DefaultBeanFactory) DestroySingletons() error {
	f.creationOrderMu.Lock()
	order := make([]string, len(f.creationOrder))
	copy(order, f.creationOrder)
	f.creationOrderMu.Unlock()
	// 逆序：后创建的（多为依赖者）先销毁
	for i := len(order) - 1; i >= 0; i-- {
		f.destroySingleton(order[i])
	}
	return nil
}

func (f *DefaultBeanFactory) destroySingleton(name string) {
	obj, ok := f.registry.getSingleton(name)
	if !ok {
		return
	}
	bd, err := f.GetBeanDefinition(name)
	if err == nil && bd.DestroyMethod != "" {
		v := reflect.ValueOf(obj)
		if m := v.MethodByName(bd.DestroyMethod); m.IsValid() {
			m.Call(nil)
		}
	}
	if d, ok := obj.(DisposableBean); ok {
		_ = d.Destroy()
	}
	f.registry.removeSingleton(name)
}

// ---- 依赖图与创建顺序 ----

func (f *DefaultBeanFactory) recordDep(from, to string) {
	f.depsMu.Lock()
	if f.deps[from] == nil {
		f.deps[from] = make(map[string]struct{})
	}
	f.deps[from][to] = struct{}{}
	f.depsMu.Unlock()
}

func (f *DefaultBeanFactory) recordCreation(name string) {
	f.creationOrderMu.Lock()
	f.creationOrder = append(f.creationOrder, name)
	f.creationOrderMu.Unlock()
}

// setField 反射设值，处理类型兼容（指针↔接口等）
func setField(field reflect.Value, val any) error {
	rv := reflect.ValueOf(val)
	if rv.IsValid() {
		if rv.Type().AssignableTo(field.Type()) {
			field.Set(rv)
			return nil
		}
		// 尝试解引用：注入的 val 是 *Concrete，字段是 *Concrete 或接口
		if rv.Kind() == reflect.Ptr {
			if rv.Type().Elem().Kind() == reflect.Struct {
				if rv.Type().AssignableTo(field.Type()) {
					field.Set(rv)
					return nil
				}
			}
		}
	}
	return fmt.Errorf("%w: cannot assign %T to %s", ErrBeanNotOfRequiredType, val, field.Type())
}

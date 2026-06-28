// Copyright All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package godi

import (
	"errors"
	"reflect"
)

// 哨兵错误，对应 Spring BeansException 体系，调用方可通过 errors.Is 精确判别
var (
	ErrNoSuchBeanDefinition  = errors.New("no such bean definition")
	ErrBeanDefinitionStore   = errors.New("bean definition store error")
	ErrBeanCurrentlyInCreation = errors.New("bean currently in creation") // 对应 Spring BeanCurrentlyInCreationException
	ErrUnsatisfiedDependency = errors.New("unsatisfied dependency")
	ErrBeanCreation          = errors.New("bean creation error")
	ErrBeanNotOfRequiredType = errors.New("bean not of required type") // 对应 BeanNotOfRequiredTypeException
	ErrNoUniqueBean          = errors.New("no unique bean definition") // 对应 NoUniqueBeanDefinitionException
	ErrCircularDependency    = errors.New("circular dependency detected")
	ErrInitFailed            = errors.New("initializer failed")
	ErrNotSupportedType      = errors.New("unsupported type")
)

// AutowireMode 自动装配模式，对应 Spring AutowireCapableBeanFactory 常量
type AutowireMode int

const (
	AutowireNo          AutowireMode = iota // 不自动装配
	AutowireByName                          // 按字段名匹配 bean 名
	AutowireByType                          // 按字段类型匹配 bean
)

// PropertyValue 属性值声明，对应 Spring BeanDefinition 的 propertyValues。
// 用于 setter/field 注入：Ref 引用另一个 bean，Value 是直接值。
type PropertyValue struct {
	Name  string         // 字段名（导出字段名）
	Ref   string         // 引用的 bean 名（优先级高于 Value）
	Value any            // 直接注入的值
}

// BeanDefinition bean 定义元数据，对应 Spring BeanDefinition。
// 描述 bean 的构造方式、作用域、依赖、生命周期方法等全部元信息。
type BeanDefinition struct {
	Name          string
	Type          reflect.Type // bean 的具体类型（用于 byType 查找与反射构造）
	Scope         string       // singleton/prototype/自定义，默认 singleton
	LazyInit      bool         // lazy-init，默认 false（Spring 默认非懒加载）
	Primary       bool         // @Primary，byType 多候选时优先
	DependsOn     []string     // depends-on，显式声明先于本 bean 构造的 bean
	Factory       SupplierFunc // 构造工厂；与 Constructor 互斥
	Constructor   func(b BeanFactory) (any, error) // 显式构造器（兼容旧 factory 模式）
	Autowire      AutowireMode // 自动装配模式
	PropertyValues []PropertyValue // 显式属性注入
	InitMethod    string       // init-method，反射调用指定方法名（无参，返回 error 时视为失败）
	DestroyMethod string       // destroy-method
	Order         int          // @Order，集合注入与 PostProcessor 排序
}

// Defaults 为 BeanDefinition 填充默认值（对应 Spring 的默认值解析）
func (bd *BeanDefinition) applyDefaults() {
	if bd.Scope == "" {
		bd.Scope = ScopeSingleton
	}
}

// IsSingleton 是否单例
func (bd *BeanDefinition) IsSingleton() bool { return bd.Scope == ScopeSingleton }

// IsPrototype 是否原型
func (bd *BeanDefinition) IsPrototype() bool { return bd.Scope == ScopePrototype }

// BeanFactory DI 容器核心接口，对应 Spring BeanFactory + ListableBeanFactory + AutowireCapableBeanFactory + HierarchicalBeanFactory。
type BeanFactory interface {
	// 注册
	RegisterBeanDefinition(name string, bd *BeanDefinition)
	RegisterAlias(alias, name string)
	RemoveBeanDefinition(name string) bool
	GetBeanDefinition(name string) (*BeanDefinition, error)
	ContainsBean(name string) bool

	// 获取 bean
	GetBean(name string) (any, error)
	GetBeanByType(t reflect.Type) (any, error)
	GetBeansOfType(t reflect.Type) (map[string]any, error)
	GetBeanNamesForType(t reflect.Type) []string
	// 类型查询（含父容器）
	GetBeanNamesForTypeAll(t reflect.Type) []string

	// 别名
	GetAliases(name string) []string

	// 层级
	GetParentBeanFactory() BeanFactory

	// 作用域
	RegisterScope(name string, scope Scope)
	GetScope(name string) (Scope, bool)

	// 生命周期
	PreInstantiateSingletons() error // refresh 阶段预实例化非懒加载单例
	DestroySingletons() error        // 关闭阶段逆序销毁单例

	// 扩展点
	AddBeanPostProcessor(p BeanPostProcessor)
	GetBeanPostProcessors() []BeanPostProcessor

	// 自动装配能力（供 factory 内部递归依赖注入用）
	// resolveDependency 按 type 解析唯一候选 bean（含父容器）
	ResolveDependency(t reflect.Type, requestingBeanName string) (any, error)
	// AutowireCapable: 反射创建并注入（不经过缓存，用于非容器管理对象）
	// 此处省略，保持核心精简
}

// SupplierFunc bean 构造供应者，对应 Spring Supplier / ObjectFactory。
// 接收 BeanFactory 以便解析依赖。
type SupplierFunc func(b BeanFactory) (any, error)

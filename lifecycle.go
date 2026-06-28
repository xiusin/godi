// Copyright All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package godi

import "reflect"

// InitializingBean 构造完成、属性注入完成后回调，对应 Spring InitializingBean。
// 实现该接口的 bean 在 initializeBean 阶段被调用。
type InitializingBean interface {
	AfterPropertiesSet() error
}

// DisposableBean 容器销毁时回调，对应 Spring DisposableBean。
type DisposableBean interface {
	Destroy() error
}

// BeanPostProcessor bean 后置处理器，对应 Spring BeanPostProcessor，是 AOP 等扩展的基石。
//   - PostProcessBeforeInitialization：在 initMethod/AfterPropertiesSet 之前调用
//   - PostProcessAfterInitialization：在初始化完成之后调用（AOP 代理通常在此生成）
//
// 处理器可返回替换后的 bean（如代理），返回 nil 表示移除该 bean。
type BeanPostProcessor interface {
	PostProcessBeforeInitialization(bean any, name string) (any, error)
	PostProcessAfterInitialization(bean any, name string) (any, error)
}

// Ordered 排序接口，对应 Spring Ordered。BeanPostProcessor 按 Order 升序应用。
// 未实现 Ordered 的处理器视为最高优先级（Order=0）。
type Ordered interface {
	Order() int
}

// orderOf 获取处理器的顺序值
func orderOf(p BeanPostProcessor) int {
	if o, ok := p.(Ordered); ok {
		return o.Order()
	}
	return 0
}

// ---- 自动装配类型识别辅助 ----

// autowireCandidate 判断类型是否可作为自动装配候选（必须是指针/接口/可设置类型）
func autowireCandidate(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func:
		return true
	}
	return false
}

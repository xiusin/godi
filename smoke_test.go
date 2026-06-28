package godi

import (
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
)

// 快速回归测试：核心功能是否正常
type circA struct{ B *circB }
type circB struct{ A *circA }

func TestSmoke_CircularDependency(t *testing.T) {
	f := NewDefaultBeanFactory()
	f.RegisterBeanDefinition("a", &BeanDefinition{
		Name: "a", Type: reflect.TypeOf((*circA)(nil)), Autowire: AutowireByType,
		Factory: func(_ BeanFactory) (any, error) { return &circA{}, nil },
	})
	f.RegisterBeanDefinition("b", &BeanDefinition{
		Name: "b", Type: reflect.TypeOf((*circB)(nil)), Autowire: AutowireByType,
		Factory: func(_ BeanFactory) (any, error) { return &circB{}, nil },
	})
	a, err := f.GetBean("a")
	if err != nil {
		t.Fatal(err)
	}
	av := a.(*circA)
	if av.B == nil || av.B.A == nil || av.B.A != av {
		t.Fatal("circular dependency not resolved properly")
	}
}

type Greeter interface{ Hello() string }
type greetHi struct{}

func (greetHi) Hello() string { return "hi" }

func TestSmoke_ByTypeWithInterface(t *testing.T) {
	f := NewDefaultBeanFactory()
	f.RegisterBeanDefinition("greeter", &BeanDefinition{
		Name: "greeter", Type: reflect.TypeOf((*Greeter)(nil)).Elem(), Primary: true,
		Factory: func(_ BeanFactory) (any, error) { return greetHi{}, nil },
	})
	g, err := f.GetBeanByType(reflect.TypeOf((*Greeter)(nil)).Elem())
	if err != nil {
		t.Fatal(err)
	}
	if g.(Greeter).Hello() != "hi" {
		t.Fatal("wrong greeting")
	}
}

func TestSmoke_ParentChild(t *testing.T) {
	p := NewDefaultBeanFactory()
	p.RegisterBeanDefinition("db", &BeanDefinition{
		Name: "db", Type: reflect.TypeOf((*DB1)(nil)),
		Factory: func(_ BeanFactory) (any, error) { return &DB1{Name: "x"}, nil },
	})
	c := NewDefaultBeanFactory(p)
	c.RegisterBeanDefinition("svc", &BeanDefinition{
		Name: "svc", Type: reflect.TypeOf((*Svc1)(nil)), Autowire: AutowireByType,
		Factory: func(_ BeanFactory) (any, error) { return &Svc1{}, nil },
	})
	v, err := c.GetBean("svc")
	if err != nil {
		t.Fatal(err)
	}
	svc := v.(*Svc1)
	if svc.DB == nil || svc.DB.Name != "x" {
		t.Fatal("parent bean not injected into child")
	}
	_, err = p.GetBean("svc")
	if !errors.Is(err, ErrNoSuchBeanDefinition) {
		t.Fatal("parent should not see child bean")
	}
}

func TestSmoke_Lifecycle(t *testing.T) {
	f := NewDefaultBeanFactory()
	var inited, destroyed atomic.Bool
	type Lc struct{}
	f.RegisterBeanDefinition("lc", &BeanDefinition{
		Name: "lc", Type: reflect.TypeOf((*Lc)(nil)),
		Factory: func(_ BeanFactory) (any, error) { return &LcHook{inited: &inited, destroyed: &destroyed}, nil },
	})
	_, _ = f.GetBean("lc")
	if !inited.Load() {
		t.Fatal("AfterPropertiesSet should be called")
	}
	_ = f.DestroySingletons()
	if !destroyed.Load() {
		t.Fatal("Destroy should be called")
	}
}

type DB1 struct{ Name string }
type Svc1 struct{ DB *DB1 }

type LcHook struct {
	inited    *atomic.Bool
	destroyed *atomic.Bool
}

func (l *LcHook) AfterPropertiesSet() error { l.inited.Store(true); return nil }
func (l *LcHook) Destroy() error            { l.destroyed.Store(true); return nil }

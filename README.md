# godi — Go 依赖注入容器（Spring 风格）

`godi` 是一个生产级 Go 依赖注入容器，深度复刻 Spring IoC 的核心机制，包含 **三级缓存解决 setter 循环依赖**、BeanPostProcessor 扩展点、byType 自动装配、父子容器、生命周期回调等完整能力。

---

## 目录

- [快速开始](#快速开始)
- [核心概念](#核心概念)
- [基础使用](#基础使用)
  - [注册 Bean](#注册-bean)
  - [获取 Bean](#获取-bean)
  - [依赖注入](#依赖注入)
  - [作用域](#作用域)
- [高级特性](#高级特性)
  - [三级缓存与循环依赖](#三级缓存与循环依赖)
  - [BeanPostProcessor 扩展](#beanpostprocessor-扩展)
  - [父子容器](#父子容器)
  - [别名](#别名)
  - [byType 与 @Primary](#bytype-与-primary)
  - [Refresh / Close 生命周期](#refresh--close-生命周期)
  - [自定义作用域](#自定义作用域)
  - [泛型 API](#泛型-api)
- [错误处理](#错误处理)
- [性能特点](#性能特点)
- [完整示例](#完整示例)

---

## 快速开始

### 安装

```go
import "github.com/xiusin/godi"
```

### 最简使用

```go
package main

import "github.com/xiusin/godi"

type DB struct { DSN string }

func main() {
    // 注册单例
    godi.RegisterSingleton("db", func(b godi.BeanFactory) (any, error) {
        return &DB{DSN: "mysql://..."}, nil
    })

    // 获取
    db := godi.MustGetBean("db").(*DB)
    println(db.DSN)
}
```

---

## 核心概念

| 概念 | 对应 Spring | 说明 |
|---|---|---|
| `BeanFactory` | `BeanFactory` + `ListableBeanFactory` | 容器核心接口 |
| `BeanDefinition` | `BeanDefinition` | bean 元数据（作用域、工厂、注入方式等） |
| `Scope` | `Scope` | 作用域：singleton / prototype / 自定义 |
| `BeanPostProcessor` | `BeanPostProcessor` | bean 初始化前后钩子（AOP 基础） |
| `InitializingBean` | `InitializingBean` | 构造完成后回调 |
| `DisposableBean` | `DisposableBean` | 销毁时回调 |
| 三级缓存 | `singletonObjects` / `earlySingletonObjects` / `singletonFactories` | 解决 setter 循环依赖 |

---

## 基础使用

> **重要约定**：使用 `AutowireByType`、`GetBeanByType` 等按类型查找的功能时，**必须在 `BeanDefinition` 中显式设置 `Type` 字段**。容器无法在实例化前从工厂函数推断返回类型。设置方式：
> ```go
> Type: reflect.TypeOf((*YourType)(nil)),   // 指针类型
> Type: reflect.TypeOf((*YourInterface)(nil)).Elem(), // 接口类型
> ```
> 实例化后容器会自动回填 `Type` 字段（用于兜底），但显式设置是最佳实践。

### 注册 Bean

#### 方式一：工厂函数（最常用）

```go
// 单例
godi.RegisterSingleton("db", func(b godi.BeanFactory) (any, error) {
    return &DB{DSN: "root:pass@tcp(localhost:3306)/test"}, nil
})

// 原型（每次获取新建）
godi.RegisterPrototype("requestCtx", func(b godi.BeanFactory) (any, error) {
    return &RequestCtx{ID: uuid.New()}, nil
})
```

#### 方式二：BeanDefinition 完整配置

```go
godi.RegisterBean("userService", &godi.BeanDefinition{
    Name:         "userService",
    Type:         reflect.TypeOf((*UserService)(nil)),
    Scope:        godi.ScopeSingleton,
    LazyInit:     false,                 // 非懒加载，Refresh 时预实例化
    Primary:      true,                  // byType 多候选时优先
    DependsOn:    []string{"db"},        // 显式依赖，确保 db 先构造
    Autowire:     godi.AutowireByType,   // 自动按类型注入字段
    PropertyValues: []godi.PropertyValue{
        {Name: "Db", Ref: "db"},        // 显式字段引用
        {Name: "Timeout", Value: 30},   // 直接值注入
    },
    InitMethod:   "Init",                // init-method
    DestroyMethod: "Close",              // destroy-method
    Order:        1,                     // 排序
    Factory: func(b godi.BeanFactory) (any, error) {
        return &UserService{}, nil
    },
})
```

#### 方式三：注册已有实例

```go
cfg := &Config{Port: 8080}
godi.RegisterInstance("config", cfg)
```

#### 方式四：构造器注入（显式调用 GetBean）

```go
godi.RegisterBean("orderService", &godi.BeanDefinition{
    Name:  "orderService",
    Scope: godi.ScopeSingleton,
    Constructor: func(b godi.BeanFactory) (any, error) {
        db, err := b.GetBean("db")
        if err != nil {
            return nil, err
        }
        return NewOrderService(db.(*DB)), nil
    },
})
```

> **注意**：构造器注入的循环依赖无法解决（与 Spring 一致），会报 `ErrBeanCurrentlyInCreation`。
> 推荐使用字段/setter 注入（Autowire + 三级缓存），循环依赖可自动解决。

### 获取 Bean

#### 按名称获取

```go
db, err := godi.GetBean("db")
if err != nil {
    // 处理错误
}

// 失败直接 panic
db := godi.MustGetBean("db").(*DB)
```

#### 按类型获取（byType）

```go
db, err := godi.GetBeanByType(reflect.TypeOf((*DB)(nil)))
```

#### 获取某类型所有 bean（集合注入）

```go
beans, err := godi.GetBeansOfType(reflect.TypeOf((*Plugin)(nil)).Elem())
```

#### 检查是否存在

```go
if godi.ContainsBean("db") {
    // ...
}
```

### 依赖注入

#### AutowireByType 自动按类型注入

```go
type UserService struct {
    DB  *DB          // 导出字段，按类型自动注入
    Log Logger       // 接口类型同样支持
    private *DB      // 未导出字段会被跳过
    Cfg *Config     // 已赋值的字段不会被覆盖
}

func NewUserService() *UserService {
    return &UserService{}
}

// 注册时启用自动装配
godi.RegisterBean("userService", &godi.BeanDefinition{
    Name:     "userService",
    Type:     reflect.TypeOf((*UserService)(nil)),
    Scope:    godi.ScopeSingleton,
    Autowire: godi.AutowireByType, // 开启按类型自动注入
    Factory: func(_ godi.BeanFactory) (any, error) {
        return &UserService{}, nil
    },
})
```

#### AutowireByName 按名称注入

```go
godi.RegisterBean("svc", &godi.BeanDefinition{
    Name:     "svc",
    Autowire: godi.AutowireByName, // 字段名 = bean 名
    // ...
})
```

#### PropertyValues 显式属性注入

```go
godi.RegisterBean("svc", &godi.BeanDefinition{
    PropertyValues: []godi.PropertyValue{
        {Name: "Db", Ref: "masterDB"},   // 引用另一个 bean
        {Name: "Port", Value: 8080},     // 直接值
        {Name: "Tags", Value: []string{"a", "b"}},
    },
    // ...
})
```

### 作用域

| 作用域 | 常量 | 说明 |
|---|---|---|
| 单例 | `ScopeSingleton` | 全局唯一实例，默认值 |
| 原型 | `ScopePrototype` | 每次获取新建实例 |
| 自定义 | 任意字符串 | 实现 `Scope` 接口注册 |

```go
// prototype
bd := godi.RegisterPrototype("req", func(b godi.BeanFactory) (any, error) {
    return &Request{ID: genID()}, nil
})

// 或通过 BeanDefinition.Scope 设置
godi.RegisterBean("req", &godi.BeanDefinition{
    Name:    "req",
    Scope:   godi.ScopePrototype,
    Factory: factoryFn,
})
```

---

## 高级特性

### 三级缓存与循环依赖

`godi` 完整复刻 Spring 的三级缓存机制，**自动解决字段/setter 注入的循环依赖**。

```go
// A 依赖 B，B 依赖 A —— 双向循环
type A struct{ B *B }
type B struct{ A *A }

func main() {
    f := godi.NewDefaultBeanFactory()

    f.RegisterBeanDefinition("a", &godi.BeanDefinition{
        Name:     "a",
        Type:     reflect.TypeOf((*A)(nil)),
        Autowire: godi.AutowireByType, // 按类型自动注入
    })
    f.RegisterBeanDefinition("b", &godi.BeanDefinition{
        Name:     "b",
        Type:     reflect.TypeOf((*B)(nil)),
        Autowire: godi.AutowireByType,
    })

    // 成功！三级缓存提前暴露半成品引用
    a, _ := f.GetBean("a")
    println(a.(*A).B.A == a) // true —— 环形引用成立
}
```

**三级缓存工作原理**：

```
一级: singletonObjects   → 完全初始化完成的 bean
二级: earlySingletonObjects → 已实例化、未完成初始化的半成品
三级: singletonFactories  → ObjectFactory，产出 early reference

流程：
1. 实例化 A → 注册三级工厂（暴露 early reference） → 开始注入 A.B
2. 注入 A.B 触发 B 创建 → 实例化 B → 注册三级工厂 → 开始注入 B.A
3. 注入 B.A 时查三级缓存 → 命中 A 的工厂 → 产出 A 的 early reference → 晋升二级
4. B 完成初始化 → 晋升一级
5. A 继续完成注入 → 晋升一级
```

> **构造器注入循环依赖**会被拒绝（报 `ErrBeanCurrentlyInCreation`），与 Spring 行为一致。

### BeanPostProcessor 扩展

`BeanPostProcessor` 是 AOP 等扩展的基础，可在 bean 初始化前后介入，甚至替换 bean 实例。

```go
// 日志装饰器 PostProcessor
type LoggingPostProcessor struct{}

func (LoggingPostProcessor) Order() int { return 0 }

func (LoggingPostProcessor) PostProcessBeforeInitialization(bean any, name string) (any, error) {
    log.Printf("before init: %s", name)
    return bean, nil
}

func (LoggingPostProcessor) PostProcessAfterInitialization(bean any, name string) (any, error) {
    // 返回代理对象替代原 bean（AOP 动态代理就是这样做的）
    return &LoggingProxy{target: bean, name: name}, nil
}

// 注册
godi.AddBeanPostProcessor(LoggingPostProcessor{})
```

PostProcessor 按 `Order()` 升序依次应用。

### 父子容器

子容器找不到 bean 时委托父容器查找（对应 Spring `HierarchicalBeanFactory`）。

```go
parent := godi.NewDefaultBeanFactory()
parent.RegisterBeanDefinition("db", &godi.BeanDefinition{...})

child := godi.NewDefaultBeanFactory(parent)
child.RegisterBeanDefinition("userService", &godi.BeanDefinition{...})

// 子容器可拿到父容器的 db
db, _ := child.GetBean("db")

// 父容器拿不到子容器的 userService
_, err := parent.GetBean("userService") // ErrNoSuchBeanDefinition
```

典型应用：Web 服务中 `RootApplicationContext` 放共享服务，`RequestContext` 放请求作用域 bean。

### 别名

一个 bean 可以有多个名字（对应 Spring `<alias>`）。

```go
godi.RegisterBean("primaryDS", &godi.BeanDefinition{...})
godi.RegisterAlias("dataSource", "primaryDS")

// 等价
a := godi.MustGetBean("primaryDS")
b := godi.MustGetBean("dataSource")
println(a == b) // true
```

### byType 与 @Primary

同一类型有多个实现时，标记 `Primary` 的 bean 会被优先选中。

```go
type Cache interface { Get(key string) string }

type RedisCache struct{}
type MemCache struct{}

func main() {
    f := godi.NewDefaultBeanFactory()

    f.RegisterBeanDefinition("redis", &godi.BeanDefinition{
        Name: "redis", Type: reflect.TypeOf((*Cache)(nil)).Elem(),
        Factory: func(_ BeanFactory) (any, error) { return &RedisCache{}, nil },
    })

    f.RegisterBeanDefinition("mem", &godi.BeanDefinition{
        Name: "mem", Type: reflect.TypeOf((*Cache)(nil)).Elem(),
        Primary: true, // 标记为主要候选
        Factory: func(_ BeanFactory) (any, error) { return &MemCache{}, nil },
    })

    // byType 自动选中 Primary
    cache, _ := f.GetBeanByType(reflect.TypeOf((*Cache)(nil)).Elem())
    // cache 是 *MemCache
}
```

多 Primary 或无 Primary 且多候选时报 `ErrNoUniqueBean`。

### Refresh / Close 生命周期

#### Refresh 预实例化

```go
// 注册完所有 bean 后调用，预实例化所有 LazyInit=false 的单例
// 对应 Spring ApplicationContext.refresh()
if err := godi.Refresh(); err != nil {
    log.Fatalf("refresh failed: %v", err)
}
```

#### Close 销毁

```go
// 关闭容器，按依赖逆序销毁所有单例
// 对应 Spring ApplicationContext.close()
if err := godi.Close(); err != nil {
    log.Printf("close errors: %v", err)
}
```

销毁顺序：**后构造的先销毁**（依赖者先销毁，被依赖者后销毁），避免引用已销毁对象。

#### 生命周期回调

实现 `InitializingBean` 和 `DisposableBean` 接口，或指定 init/destroy 方法。

```go
type MyService struct {
    db *DB
    closed atomic.Bool
}

// 构造完成、属性注入后自动调用
func (s *MyService) AfterPropertiesSet() error {
    s.db.Ping()
    return nil
}

// 容器销毁时自动调用
func (s *MyService) Destroy() error {
    s.closed.Store(true)
    return nil
}

// 或通过 init-method / destroy-method 指定
godi.RegisterBean("svc", &godi.BeanDefinition{
    InitMethod:    "Init",
    DestroyMethod: "Shutdown",
    // ...
})
```

### 自定义作用域

实现 `Scope` 接口即可注册新作用域，例如 request 作用域：

```go
type requestScope struct {
    ctx *RequestContext
}

func (s *requestScope) Name() string { return "request" }

func (s *requestScope) Get(name string, factory func() (any, error)) (any, error) {
    if v, ok := s.ctx.Beans[name]; ok {
        return v, nil
    }
    v, err := factory()
    if err != nil {
        return nil, err
    }
    s.ctx.Beans[name] = v
    return v, nil
}

func (s *requestScope) Remove(name string) {
    delete(s.ctx.Beans, name)
}

// 注册
godi.RegisterScope("request", &requestScope{ctx: reqCtx})

// 使用
godi.RegisterBean("reqUser", &godi.BeanDefinition{
    Scope: "request",
    // ...
})
```

### 泛型 API

Go 1.18+ 提供类型安全的泛型 API，消除调用方类型断言。

```go
// 按类型获取
db, err := godi.GetBeanT[*DB](f)
if err != nil {
    return err
}
db.Query(...) // 直接使用，无需类型断言

// 失败 panic
db := godi.MustGetBeanT[*DB](f)

// 获取某类型所有 bean（泛型集合注入）
plugins, err := godi.GetBeansT[Plugin](f)
for name, p := range plugins {
    p.Run()
}
```

---

## 错误处理

所有错误均可通过 `errors.Is` 精确判别：

```go
db, err := godi.GetBean("db")
switch {
case errors.Is(err, godi.ErrNoSuchBeanDefinition):
    // bean 未注册
case errors.Is(err, godi.ErrBeanCurrentlyInCreation):
    // 构造器注入循环依赖
case errors.Is(err, godi.ErrUnsatisfiedDependency):
    // 依赖无法满足
case errors.Is(err, godi.ErrNoUniqueBean):
    // byType 多候选且无 Primary
case errors.Is(err, godi.ErrInitFailed):
    // 初始化失败
case errors.Is(err, godi.ErrBeanCreation):
    // 创建失败（顶层包装）
case err != nil:
    // 其他错误
}
```

---

## 性能特点

| 场景 | 耗时 | 分配 | 说明 |
|---|---|---|---|
| 单例 Get（热路径） | ~30 ns | 0 | 一级缓存命中，读读不互斥 |
| 并发 Get | ~30 ns | 0 | per-bean 锁，不同 bean 互不阻塞 |
| prototype Get | ~100 ns | 2 alloc | 工厂创建 + 作用域查询 |
| 三级缓存循环依赖 | ~200 ns | 少量 | early reference 生成 + 晋升 |

**无全局强锁**：稳态下不同 bean 的 Get 完全并行，仅同 bean 首次构造时持 per-bean 锁。

---

## 可运行示例

`examples/` 目录下包含 4 个可直接运行的完整示例：

| 示例 | 路径 | 演示内容 |
|---|---|---|
| basic | `examples/basic/main.go` | 完整使用流程：注册、自动装配、生命周期回调、Refresh/Close |
| circular | `examples/circular/main.go` | 三级缓存解决 A↔B 双向循环依赖 |
| postprocessor | `examples/postprocessor/main.go` | BeanPostProcessor 实现日志装饰器（AOP 代理） |
| hierarchical | `examples/hierarchical/main.go` | 父子容器（父共享服务 + 子请求级服务） |

运行方式：
```bash
go run ./examples/basic
go run ./examples/circular
go run ./examples/postprocessor
go run ./examples/hierarchical
```

---

## 完整示例

```go
package main

import (
    "fmt"
    "reflect"
    "github.com/xiusin/godi"
)

// ---- 业务类型 ----
type DB struct { DSN string }

type Logger interface { Log(msg string) }
type StdLogger struct{}
func (StdLogger) Log(msg string) { fmt.Println(msg) }

type UserService struct {
    DB   *DB
    Log  Logger
}

func (u *UserService) AfterPropertiesSet() error {
    u.Log.Log("UserService initialized")
    return nil
}

func (u *UserService) Destroy() error {
    u.Log.Log("UserService destroyed")
    return nil
}

// ---- 主函数 ----
func main() {
    f := godi.NewDefaultBeanFactory()

    // 注册 DB
    f.RegisterBeanDefinition("db", &godi.BeanDefinition{
        Name: "db", Type: reflect.TypeOf((*DB)(nil)),
        Factory: func(_ godi.BeanFactory) (any, error) {
            return &DB{DSN: "mysql://localhost:3306/test"}, nil
        },
    })

    // 注册 Logger（Primary）
    f.RegisterBeanDefinition("logger", &godi.BeanDefinition{
        Name: "logger", Type: reflect.TypeOf((*Logger)(nil)).Elem(),
        Primary: true,
        Factory: func(_ godi.BeanFactory) (any, error) {
            return StdLogger{}, nil
        },
    })

    // 注册 UserService：自动按类型注入 + 生命周期回调
    f.RegisterBeanDefinition("userService", &godi.BeanDefinition{
        Name:     "userService",
        Type:     reflect.TypeOf((*UserService)(nil)),
        Autowire: godi.AutowireByType,
        Factory: func(_ godi.BeanFactory) (any, error) {
            return &UserService{}, nil
        },
    })

    // 预实例化非懒加载单例
    if err := f.PreInstantiateSingletons(); err != nil {
        panic(err)
    }

    // 使用
    svc := godi.MustGetBeanT[*UserService](f)
    fmt.Printf("DB DSN: %s\n", svc.DB.DSN)
    svc.Log.Log("hello")

    // 关闭
    f.DestroySingletons()
}
```

---

## License

MIT
